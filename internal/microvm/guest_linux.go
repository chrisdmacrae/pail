//go:build linux

package microvm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// guestInit is where Pail's own binary sits inside every root filesystem it
// makes. A microVM boots it as PID 1.
const guestInit = "/pail-init"

// Where things are inside the guest.
const (
	guestWork = "/work"            // the work disk
	guestJob  = "/work/.pail/job"  // what to run, from the host
	guestExit = "/work/.pail/exit" // how it ended, for the host
	guestTmp  = "/work/.pail/tmp"  // what the job sees as /tmp
)

// A container's microVM is told apart from a job's on the kernel's command
// line, which the kernel hands to init as its environment.
const (
	guestModeArg     = "pailmode"
	guestModeService = "service"
	// A function's microVM runs an agent that takes requests from the host.
	guestModeFunction = "function"
	// guestAgentPort is the vsock port the agent listens on.
	guestAgentPort = 1024
	// guestServiceJob is what a container's microVM runs, left in its own
	// root filesystem by the host.
	guestServiceJob = "/.pail-job"
	// guestControlPort is where init listens for the host asking it to
	// stop. Only the host can reach a guest, and nothing else uses port 1.
	guestControlPort = 1
)

// guestJobSpec is what the host asks the guest to run.
type guestJobSpec struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
	// The rest is for containers. User is as an image names it; Data is
	// where the data volume mounts, if there is one.
	User     string `json:"user,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Data     string `json:"data,omitempty"`
	// Hosts are the machines this one may reach, and itself: each one's
	// address by its name.
	Hosts map[string]string `json:"hosts,omitempty"`
}

// IsGuestInit reports whether this process is PID 1 inside a microVM: Pail's
// binary, booted by the guest kernel as init.
func IsGuestInit() bool {
	return os.Getpid() == 1 && len(os.Args) > 0 && os.Args[0] == guestInit
}

// GuestMain is PID 1 inside a microVM. It brings the guest up far enough to
// run one job from the work disk, records how the job ended, and powers the
// machine off. Everything it prints reaches the host through the serial
// console.
func GuestMain() {
	switch os.Getenv(guestModeArg) {
	case guestModeService:
		serviceMain()
		return
	case guestModeFunction:
		functionMain()
		return
	}
	code := 1
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "pail-init:", r)
		}
		os.WriteFile(guestExit, []byte(strconv.Itoa(code)), 0o644)
		syscall.Sync()
		syscall.Unmount("/tmp", 0)
		syscall.Unmount(guestWork, 0)
		powerOff()
	}()

	// The root filesystem is read-only and shared by every microVM made
	// from the same image; what a job writes goes to the work disk.
	mounts := []struct{ src, dst, kind, opts string }{
		{"proc", "/proc", "proc", ""},
		{"sysfs", "/sys", "sysfs", ""},
		{"devtmpfs", "/dev", "devtmpfs", ""},
		{"tmpfs", "/run", "tmpfs", "mode=755"},
		{"/dev/vdb", guestWork, "ext4", ""},
	}
	for _, m := range mounts {
		err := syscall.Mount(m.src, m.dst, m.kind, 0, m.opts)
		// The kernel may have mounted /dev itself.
		if err != nil && err != syscall.EBUSY {
			panic(fmt.Sprintf("mount %s: %v", m.dst, err))
		}
	}
	// /tmp lives on the work disk, not in memory: package managers cache
	// gigabytes there, and the machine's memory is for the job.
	if err := os.MkdirAll(guestTmp, 0o1777); err != nil {
		panic(fmt.Sprintf("make /tmp: %v", err))
	}
	os.Chmod(guestTmp, 0o1777|os.ModeSticky)
	if err := syscall.Mount(guestTmp, "/tmp", "", syscall.MS_BIND, ""); err != nil {
		panic(fmt.Sprintf("mount /tmp: %v", err))
	}
	syscall.Sethostname([]byte("pail"))
	loopbackUp()

	raw, err := os.ReadFile(guestJob)
	if err != nil {
		panic(fmt.Sprintf("no job on the work disk: %v", err))
	}
	var job guestJobSpec
	if err := json.Unmarshal(raw, &job); err != nil || len(job.Argv) == 0 {
		panic(fmt.Sprintf("the job can't be read: %v", err))
	}

	cmd := exec.Command(job.Argv[0], job.Argv[1:]...)
	cmd.Env, cmd.Dir = job.Env, job.Dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	switch err := cmd.Run().(type) {
	case nil:
		code = 0
	case *exec.ExitError:
		code = err.ExitCode()
	default:
		fmt.Fprintln(os.Stderr, "pail-init:", err)
		code = 127
	}
}

// serviceMain is PID 1 inside a container's microVM. The root filesystem
// is the machine's own and writable; init brings the guest up, runs the
// image's command, and powers off when it ends or the host asks it to stop.
func serviceMain() {
	var data string
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "pail:", r)
		}
		// Whatever is still running goes before the disks do.
		syscall.Kill(-1, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		syscall.Kill(-1, syscall.SIGKILL)
		syscall.Sync()
		if data != "" {
			syscall.Unmount(data, 0)
		}
		syscall.Mount("", "/", "", syscall.MS_REMOUNT|syscall.MS_RDONLY, "")
		powerOff()
	}()

	for _, dir := range []string{"/proc", "/sys", "/dev", "/run", "/tmp"} {
		os.MkdirAll(dir, 0o755)
	}
	mounts := []struct{ src, dst, kind, opts string }{
		{"proc", "/proc", "proc", ""},
		{"sysfs", "/sys", "sysfs", ""},
		{"devtmpfs", "/dev", "devtmpfs", ""},
		{"devpts", "/dev/pts", "devpts", "mode=620,ptmxmode=666"},
		{"tmpfs", "/dev/shm", "tmpfs", "mode=1777"},
		{"tmpfs", "/run", "tmpfs", "mode=755"},
	}
	for _, m := range mounts {
		os.MkdirAll(m.dst, 0o755)
		err := syscall.Mount(m.src, m.dst, m.kind, 0, m.opts)
		if err != nil && err != syscall.EBUSY {
			panic(fmt.Sprintf("mount %s: %v", m.dst, err))
		}
	}
	os.Chmod("/tmp", 0o777|os.ModeSticky)
	loopbackUp()

	raw, err := os.ReadFile(guestServiceJob)
	if err != nil {
		panic(fmt.Sprintf("nothing to run: %v", err))
	}
	os.Remove(guestServiceJob)
	var job guestJobSpec
	if err := json.Unmarshal(raw, &job); err != nil || len(job.Argv) == 0 {
		panic(fmt.Sprintf("what to run can't be read: %v", err))
	}
	if job.Hostname != "" {
		syscall.Sethostname([]byte(job.Hostname))
	}
	if err := nameHosts(job.Hosts); err != nil {
		panic(fmt.Sprintf("name the pail's other containers: %v", err))
	}

	who, err := resolveUser(job.User)
	if err != nil {
		panic(err.Error())
	}
	if job.Data != "" {
		if err := os.MkdirAll(job.Data, 0o755); err != nil {
			panic(fmt.Sprintf("make %s: %v", job.Data, err))
		}
		if err := syscall.Mount("/dev/vdb", job.Data, "ext4", 0, ""); err != nil {
			panic(fmt.Sprintf("mount the data volume at %s: %v", job.Data, err))
		}
		data = job.Data
		// The volume starts out root's; the app has to be able to write it.
		os.Chown(job.Data, int(who.uid), int(who.gid))
	}

	env := job.Env
	if who.home != "" && !hasEnv(env, "HOME") {
		env = append(env, "HOME="+who.home)
	}
	if !hasEnv(env, "PATH") {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	dir := job.Dir
	if dir == "" {
		dir = "/"
	}
	os.MkdirAll(dir, 0o755)
	program, err := findProgram(job.Argv[0], dir, env)
	if err != nil {
		panic(err.Error())
	}
	cmd := &exec.Cmd{Path: program, Args: job.Argv, Env: env, Dir: dir, Stdout: os.Stdout, Stderr: os.Stderr}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if who.uid != 0 || who.gid != 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: who.uid, Gid: who.gid, Groups: who.groups}
	}

	// Listen before the app starts, so it can't take the port first.
	control, err := net.Listen("tcp", fmt.Sprintf(":%d", guestControlPort))
	if err != nil {
		panic(fmt.Sprintf("listen for the host: %v", err))
	}
	if err := cmd.Start(); err != nil {
		panic(fmt.Sprintf("can't run %s: %v", job.Argv[0], err))
	}
	go func() {
		for {
			conn, err := control.Accept()
			if err != nil {
				return
			}
			word, _ := bufio.NewReader(conn).ReadString('\n')
			conn.Close()
			if strings.TrimSpace(word) != "stop" {
				continue
			}
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			time.Sleep(StopGrace)
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	// As PID 1, init inherits every orphan, and has to wait for them all.
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || pid != cmd.Process.Pid {
			if err != nil {
				return
			}
			continue
		}
		switch {
		case status.Signaled():
			fmt.Printf("pail: %s was stopped by %s\n", filepath.Base(job.Argv[0]), status.Signal())
		case status.ExitStatus() != 0:
			fmt.Printf("pail: %s exited with status %d\n", filepath.Base(job.Argv[0]), status.ExitStatus())
		default:
			fmt.Printf("pail: %s finished\n", filepath.Base(job.Argv[0]))
		}
		return
	}
}

// who is the user a container's command runs as.
type who struct {
	uid, gid uint32
	groups   []uint32
	home     string
}

// nameHosts adds the machines this one may reach to /etc/hosts, so each
// answers to its name.
func nameHosts(hosts map[string]string) error {
	if len(hosts) == 0 {
		return nil
	}
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	// The file may not end in a newline, so start with one.
	lines := "\n"
	for _, name := range names {
		lines += hosts[name] + " " + name + "\n"
	}
	f, err := os.OpenFile("/etc/hosts", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(lines); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// resolveUser reads an image's USER: "", a name, an ID, or either with a
// group after a colon. Names are looked up in the image's own /etc/passwd.
func resolveUser(spec string) (who, error) {
	w := who{home: "/root"}
	if spec == "" {
		return w, nil
	}
	name, group, _ := strings.Cut(spec, ":")
	u, err := user.Lookup(name)
	if err != nil {
		u, err = user.LookupId(name)
	}
	switch {
	case err == nil:
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		w.uid, w.gid, w.home = uint32(uid), uint32(gid), u.HomeDir
		if ids, err := u.GroupIds(); err == nil {
			for _, id := range ids {
				if n, err := strconv.ParseUint(id, 10, 32); err == nil {
					w.groups = append(w.groups, uint32(n))
				}
			}
		}
	default:
		// An ID the image has no entry for is still a user to run as.
		uid, perr := strconv.ParseUint(name, 10, 32)
		if perr != nil {
			return w, fmt.Errorf("the image runs as user %q, and it has no such user", name)
		}
		w.uid, w.gid, w.home = uint32(uid), uint32(uid), "/"
	}
	if group != "" {
		if g, err := user.LookupGroup(group); err == nil {
			group = g.Gid
		}
		gid, err := strconv.ParseUint(group, 10, 32)
		if err != nil {
			return w, fmt.Errorf("the image runs as group %q, and it has no such group", group)
		}
		w.gid = uint32(gid)
	}
	return w, nil
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

// findProgram finds a command the way a shell would, with the image's PATH.
func findProgram(name, dir string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return name, nil
	}
	// The last PATH wins, as it does when the command runs.
	for i := len(env) - 1; i >= 0; i-- {
		search, ok := strings.CutPrefix(env[i], "PATH=")
		if !ok {
			continue
		}
		for _, folder := range filepath.SplitList(search) {
			candidate := filepath.Join(folder, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
		break
	}
	return "", fmt.Errorf("the image has no %s on its PATH", name)
}

// loopbackUp brings up lo, which the kernel leaves down.
func loopbackUp() {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

// powerOff stops the machine, which ends the Firecracker process on the host.
func powerOff() {
	how := syscall.LINUX_REBOOT_CMD_POWER_OFF
	if runtime.GOARCH == "amd64" {
		// On x86 Firecracker exits on a reset, not a power-off.
		how = syscall.LINUX_REBOOT_CMD_RESTART
	}
	syscall.Reboot(how)
	select {}
}

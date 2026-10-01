//go:build linux

package microvm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
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

// guestJobSpec is what the host asks the guest to run.
type guestJobSpec struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
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

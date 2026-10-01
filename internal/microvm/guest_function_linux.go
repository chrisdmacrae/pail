//go:build linux

package microvm

import (
	"bufio"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// functionMain is PID 1 inside a function's microVM: the agent. It brings
// the guest up and then waits on a vsock port for the host to send requests,
// running the function's program afresh for each one. Nothing here ends by
// itself: the host stops the microVM when the function has been idle.
func functionMain() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "pail:", r)
		}
		powerOff()
	}()

	// Both disks are read-only, and shared by every copy of the function.
	// What a program writes goes to /tmp, in memory, and lasts as long as
	// the copy does.
	mounts := []struct {
		src, dst, kind string
		flags          uintptr
		opts           string
	}{
		{"proc", "/proc", "proc", 0, ""},
		{"sysfs", "/sys", "sysfs", 0, ""},
		{"devtmpfs", "/dev", "devtmpfs", 0, ""},
		{"tmpfs", "/run", "tmpfs", 0, "mode=755"},
		{"tmpfs", "/tmp", "tmpfs", 0, "mode=1777"},
		{"/dev/vdb", guestWork, "ext4", syscall.MS_RDONLY, ""},
	}
	for _, m := range mounts {
		err := syscall.Mount(m.src, m.dst, m.kind, m.flags, m.opts)
		if err != nil && err != syscall.EBUSY {
			panic(fmt.Sprintf("mount %s: %v", m.dst, err))
		}
	}
	syscall.Sethostname([]byte("pail"))
	loopbackUp()

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(fmt.Sprintf("open a vsock socket: %v", err))
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: guestAgentPort}); err != nil {
		panic(fmt.Sprintf("bind the agent's port: %v", err))
	}
	if err := unix.Listen(fd, 16); err != nil {
		panic(fmt.Sprintf("listen for the host: %v", err))
	}
	for {
		nfd, _, err := unix.Accept(fd)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			panic(fmt.Sprintf("accept: %v", err))
		}
		conn := os.NewFile(uintptr(nfd), "vsock")
		serveAgent(conn, "")
		conn.Close()
	}
}

// ServeAgent is the agent where there is no microVM around it: it answers
// the requests that arrive on l, one at a time, until l closes. Each has to
// carry token, since more than the host may be able to reach l.
func ServeAgent(l net.Listener, token string) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		serveAgent(conn, token)
		conn.Close()
	}
}

// frames writes the agent's answer: output as it comes, then how it ended.
type frames struct {
	mu  sync.Mutex
	w   io.Writer
	err error
	// broken is called once, when the host has gone away.
	broken func()
}

func (f *frames) send(kind byte, p []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return
	}
	var head [5]byte
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(p)))
	if _, f.err = f.w.Write(append(head[:], p...)); f.err != nil && f.broken != nil {
		f.broken()
	}
}

func (f *frames) result(r agentResult) {
	b, _ := json.Marshal(r)
	f.send(frameResult, b)
}

// serveAgent answers one request from the host. token, when set, is what
// the request has to carry.
func serveAgent(conn io.ReadWriter, token string) {
	out := &frames{w: conn}
	in := bufio.NewReader(conn)
	var size [4]byte
	if _, err := io.ReadFull(in, size[:]); err != nil {
		return
	}
	header := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(in, header); err != nil {
		return
	}
	var req agentRequest
	if err := json.Unmarshal(header, &req); err != nil {
		out.result(agentResult{Error: "the agent couldn't read the request: " + err.Error()})
		return
	}

	if token != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 {
		out.result(agentResult{Error: "the agent was asked by something that isn't its Pail"})
		return
	}

	switch req.Op {
	case "hello":
		if req.Time > 0 {
			tv := unix.NsecToTimeval(req.Time)
			unix.Settimeofday(&tv)
		}
		if req.IP != "" {
			if err := configureNet("eth0", req.IP, req.Gateway); err != nil {
				out.result(agentResult{Error: "the agent couldn't set the guest's address: " + err.Error()})
				return
			}
		}
		out.result(agentResult{})
	case "run":
		out.result(runProgram(req, in, out))
	default:
		out.result(agentResult{Error: "the agent doesn't know how to " + req.Op})
	}
}

// runProgram runs the function's program once: the request's body on its
// standard input, what it writes sent back as it comes.
func runProgram(req agentRequest, body io.Reader, out *frames) agentResult {
	if len(req.Argv) == 0 {
		return agentResult{Error: "there is nothing to run"}
	}
	dir := req.Dir
	if dir == "" {
		dir = guestWork
	}
	program, err := findProgram(req.Argv[0], dir, req.Env)
	if err != nil {
		return agentResult{Error: err.Error()}
	}
	stdin, stdinW, err := os.Pipe()
	if err != nil {
		return agentResult{Error: err.Error()}
	}
	stdoutR, stdout, _ := os.Pipe()
	stderrR, stderr, _ := os.Pipe()
	cmd := &exec.Cmd{Path: program, Args: req.Argv, Env: req.Env, Dir: dir, Stdin: stdin, Stdout: stdout, Stderr: stderr}
	// A group of its own, so everything the program starts ends with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killsBefore := oomKills()
	err = cmd.Start()
	stdin.Close()
	stdout.Close()
	stderr.Close()
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		stderrR.Close()
		return agentResult{Error: fmt.Sprintf("can't run %s: %v", req.Argv[0], err)}
	}
	pid := cmd.Process.Pid
	kill := func() { syscall.Kill(-pid, syscall.SIGKILL) }
	out.broken = kill

	go func() {
		io.CopyN(stdinW, body, req.BodyLen)
		stdinW.Close()
	}()
	var pipes sync.WaitGroup
	for kind, r := range map[byte]*os.File{frameStdout: stdoutR, frameStderr: stderrR} {
		pipes.Add(1)
		go func() {
			defer pipes.Done()
			defer r.Close()
			buf := make([]byte, 32<<10)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					out.send(kind, buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}

	var timedOut bool
	var timer *time.Timer
	if req.TimeoutMS > 0 {
		timer = time.AfterFunc(time.Duration(req.TimeoutMS)*time.Millisecond, func() {
			timedOut = true
			kill()
		})
	}
	// As PID 1 the agent inherits every orphan, and waits for them all.
	var status syscall.WaitStatus
	for {
		got, err := syscall.Wait4(-1, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || got == pid {
			break
		}
	}
	if timer != nil {
		timer.Stop()
	}
	// Whatever the program left running holds its output open.
	kill()
	pipes.Wait()

	res := agentResult{TimedOut: timedOut}
	switch {
	case status.Signaled():
		res.Signal = status.Signal().String()
		res.ExitCode = 128 + int(status.Signal())
		res.OOM = !timedOut && oomKills() > killsBefore
	default:
		res.ExitCode = status.ExitStatus()
	}
	return res
}

// oomKills is how many processes the kernel has killed for want of memory:
// in this container, where the agent runs in one, else in the whole guest.
func oomKills() int {
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.events")
	if err != nil {
		raw, err = os.ReadFile("/proc/vmstat")
	}
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if count, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(count))
			return n
		}
	}
	return 0
}

// configureNet gives the guest's network device its address, on a /30, and
// sends everything else through the gateway.
func configureNet(device, ip, gateway string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	set := func(request uint, addr string) error {
		ifr, err := unix.NewIfreq(device)
		if err != nil {
			return err
		}
		four := net.ParseIP(addr).To4()
		if four == nil {
			return fmt.Errorf("%q isn't an address", addr)
		}
		if err := ifr.SetInet4Addr(four); err != nil {
			return err
		}
		return unix.IoctlIfreq(fd, request, ifr)
	}
	if err := set(unix.SIOCSIFADDR, ip); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	if err := set(unix.SIOCSIFNETMASK, "255.255.255.252"); err != nil {
		return fmt.Errorf("netmask: %w", err)
	}
	ifr, err := unix.NewIfreq(device)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bring it up: %w", err)
	}

	// The default route, as the kernel's rtentry lays it out on 64-bit.
	var route struct {
		_       uint64
		dst     unix.RawSockaddrInet4
		gateway unix.RawSockaddrInet4
		genmask unix.RawSockaddrInet4
		flags   uint16
		_       int16
		_       uint64
		_       uintptr
		metric  int16
		dev     uintptr
		_       uint64
		_       uint64
		_       uint16
	}
	route.dst.Family, route.genmask.Family, route.gateway.Family = unix.AF_INET, unix.AF_INET, unix.AF_INET
	copy(route.gateway.Addr[:], net.ParseIP(gateway).To4())
	route.flags = unix.RTF_UP | unix.RTF_GATEWAY
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCADDRT, uintptr(unsafe.Pointer(&route)))
	if errno != 0 && errno != unix.EEXIST {
		return fmt.Errorf("default route: %w", errno)
	}
	return nil
}

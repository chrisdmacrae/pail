//go:build linux

package microvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// What a function's build gets.
const (
	functionBuildWorkMB  = 8192
	functionBuildTimeout = 15 * time.Minute
)

// functionPrelude goes before a language's build script. The root
// filesystem is read-only, so everything a tool caches goes under /work.
const functionPrelude = `set -e
cd /work/src
export HOME=/work/home CI=true NO_COLOR=1
mkdir -p "$HOME"
`

// BuildFunction makes a function ready to run: its source built, where the
// language needs that, in a throwaway microVM that can reach the internet
// and nothing else; then one copy booted and snapshotted.
func (r *Runner) BuildFunction(ctx context.Context, req FunctionBuild) (FunctionImage, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, functionBuildTimeout)
	defer cancel()
	var fn FunctionImage

	if err := os.MkdirAll(filepath.Join(r.cfg.Dir, "builds"), 0o755); err != nil {
		return fn, err
	}
	tmp, err := os.MkdirTemp(filepath.Join(r.cfg.Dir, "builds"), "f-")
	if err != nil {
		return fn, err
	}
	defer os.RemoveAll(tmp)
	stage := filepath.Join(tmp, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "src"), 0o755); err != nil {
		return fn, err
	}
	if err := req.Fill(filepath.Join(stage, "src")); err != nil {
		return fn, err
	}

	// What the function runs from: its source as it is, or as built.
	code := stage
	if req.Script != "" {
		image, err := r.Image(ctx, req.BuildImage, log)
		if err != nil {
			return fn, err
		}
		done, err := r.Run(ctx, Job{
			Image: image, Stage: stage, WorkMB: functionBuildWorkMB,
			Argv: []string{"/bin/sh", "-c", functionPrelude + req.Script}, Dir: "/",
			VCPUs: buildVCPUs, MemMB: buildMemMB, Network: true, Log: log,
		})
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			return fn, fmt.Errorf("the build ran for %s and was stopped", functionBuildTimeout)
		case err != nil:
			return fn, err
		case done.ExitCode != 0:
			return fn, fmt.Errorf("the build exited with status %d", done.ExitCode)
		}
		code = filepath.Join(tmp, "built")
		if err := r.Extract(done.Work, "/src", code); err != nil {
			return fn, err
		}
		os.Remove(done.Work)
	}

	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return fn, err
	}
	fn.Code = filepath.Join(req.Dir, "code.ext4")
	os.RemoveAll(filepath.Join(code, ".pail"))
	if err := makeExt4(ctx, code, fn.Code, 0, 0); err != nil {
		return fn, err
	}

	// The root filesystem is the run image, kept with the function: Pail's
	// own cache of images is cleared whenever Pail itself changes, and a
	// snapshot only resumes on the very filesystem it was taken on.
	base, err := r.Image(ctx, req.RunImage, log)
	if err != nil {
		return fn, err
	}
	fn.Root = filepath.Join(req.Dir, "root.ext4")
	fn.RootID = strings.TrimSuffix(filepath.Base(base.Path), ".ext4")
	fn.Env = base.Env
	os.Remove(fn.Root)
	if err := os.Link(base.Path, fn.Root); err != nil {
		if err := copyFile(base.Path, fn.Root, 0o644); err != nil {
			return fn, err
		}
	}

	warm := FunctionCall{Argv: req.Warm, Env: append(append([]string{}, fn.Env...), req.WarmEnv...), Dir: "/work/src", Timeout: 30 * time.Second, MaxOutput: 1 << 20}
	if err := r.snapshotFunction(ctx, &fn, req.MemMB, filepath.Join(tmp, "snap"), warm, log); err != nil {
		// Not every machine can: the function still runs, booting each copy
		// from cold.
		r.log.Warn("snapshot a function", "err", err)
		log("couldn't snapshot the function (" + err.Error() + "); its copies will boot from cold")
		os.Remove(filepath.Join(req.Dir, "mem"))
		os.Remove(filepath.Join(req.Dir, "state"))
		fn.Mem, fn.State = "", ""
	}
	return fn, nil
}

// snapshotFunction boots a function once, waits for the agent inside to be
// ready, and saves the paused microVM beside the function's other files.
func (r *Runner) snapshotFunction(ctx context.Context, fn *FunctionImage, memMB int, dir string, warm FunctionCall, log func(string)) error {
	c, err := r.startFunction(ctx, FunctionSpec{Dir: dir, Image: *fn, MemMB: memMB, Log: log}, true)
	if err != nil {
		return err
	}
	defer c.Stop()
	if len(warm.Argv) > 0 {
		// How it goes doesn't matter: it is only run to be read from disk.
		c.Call(ctx, warm)
	}
	api := filepath.Join(dir, functionAPI)
	if err := fcAPI(ctx, api, "PATCH", "/vm", map[string]any{"state": "Paused"}); err != nil {
		return err
	}
	keep := filepath.Dir(fn.Root)
	mem, state := filepath.Join(keep, "mem"), filepath.Join(keep, "state")
	err = fcAPI(ctx, api, "PUT", "/snapshot/create", map[string]any{"snapshot_type": "Full", "snapshot_path": state, "mem_file_path": mem})
	if err != nil {
		return err
	}
	fn.Mem, fn.State = mem, state
	return nil
}

// Names inside a function copy's folder. They are relative so that a
// snapshot taken in one folder resumes in another.
const (
	functionRoot  = "root.ext4"
	functionCode  = "code.ext4"
	functionVsock = "v.sock"
	functionAPI   = "api.sock"
)

// functionCopy is one running microVM of a function.
type functionCopy struct {
	dir    string
	tap    *tap
	vm     *booted
	cancel context.CancelFunc
	gone   chan struct{}
	stop   sync.Once
}

// StartFunction starts one copy of a function: restored from its snapshot,
// or booted from cold if it has none or the snapshot won't resume.
func (r *Runner) StartFunction(ctx context.Context, spec FunctionSpec) (FunctionCopy, error) {
	if spec.Image.State != "" {
		c, err := r.startFunction(ctx, spec, false)
		if err == nil {
			return c, nil
		}
		// A snapshot from another Firecracker, or another processor, may
		// not resume. The function's files still boot.
		r.log.Warn("restore a function's snapshot; booting it from cold", "err", err)
	}
	return r.startFunction(ctx, spec, true)
}

func (r *Runner) startFunction(ctx context.Context, spec FunctionSpec, cold bool) (*functionCopy, error) {
	os.RemoveAll(spec.Dir)
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return nil, err
	}
	fail := func(err error) (*functionCopy, error) {
		os.RemoveAll(spec.Dir)
		return nil, err
	}
	for name, file := range map[string]string{functionRoot: spec.Image.Root, functionCode: spec.Image.Code} {
		if err := os.Symlink(file, filepath.Join(spec.Dir, name)); err != nil {
			return fail(err)
		}
	}
	t, err := r.net.acquire()
	if err != nil {
		return fail(fmt.Errorf("can't set up the microVM's network: %w", err))
	}
	b := bootSpec{
		dir:    spec.Dir,
		drives: []drive{{"rootfs", functionRoot, true}, {"code", functionCode, true}},
		args:   guestModeArg + "=" + guestModeFunction,
		vcpus:  1, memMB: spec.MemMB, tap: t, bareNet: true, log: spec.Log, tied: true,
		vsock: functionVsock, api: functionAPI,
	}
	if !cold {
		b.restore = &snapshot{mem: spec.Image.Mem, state: spec.Image.State}
	}
	run, cancel := context.WithCancel(context.Background())
	vm, err := r.boot(run, b)
	if err != nil {
		cancel()
		t.release()
		return fail(err)
	}
	c := &functionCopy{dir: spec.Dir, tap: t, vm: vm, cancel: cancel, gone: make(chan struct{})}
	go func() {
		<-vm.done
		cancel()
		t.release()
		os.RemoveAll(spec.Dir)
		close(c.gone)
	}()

	// The agent inside is told who it is on this network, and the time: a
	// copy restored from a snapshot wakes up with the address and the clock
	// of the moment the snapshot was taken.
	wait := 60 * time.Second
	if !cold {
		wait = 15 * time.Second
	}
	hello := agentRequest{Op: "hello", IP: t.GuestIP, Gateway: t.HostIP, Time: time.Now().UnixNano()}
	deadline := time.Now().Add(wait)
	for {
		hello.Time = time.Now().UnixNano()
		_, err := c.ask(ctx, hello, nil, 0, nil, 0, 5*time.Second)
		if err == nil {
			return c, nil
		}
		select {
		case <-c.gone:
			return nil, fmt.Errorf("the function's microVM stopped as it started: %s", strings.TrimSpace(vm.problems.String()))
		case <-ctx.Done():
			c.Stop()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			c.Stop()
			return nil, fmt.Errorf("the agent inside the function's microVM didn't answer: %w", err)
		}
	}
}

func (c *functionCopy) Done() <-chan struct{} { return c.gone }

func (c *functionCopy) Stop() {
	c.stop.Do(c.cancel)
	<-c.gone
}

// Call runs one program in the copy and waits for it to end.
func (c *functionCopy) Call(ctx context.Context, call FunctionCall) (FunctionResult, error) {
	req := agentRequest{
		Op: "run", Argv: call.Argv, Env: call.Env, Dir: call.Dir,
		BodyLen: call.BodyLen, TimeoutMS: call.Timeout.Milliseconds(),
	}
	// The agent stops the program at its timeout; the extra is for a copy
	// that has stopped answering altogether.
	return c.ask(ctx, req, call.Body, call.BodyLen, call.Stderr, call.MaxOutput, call.Timeout+5*time.Second)
}

// Frames from the agent: a kind, a length, and that many bytes.
const (
	frameStdout = 1
	frameStderr = 2
	frameResult = 3
)

// agentRequest is what the host asks of the agent in a function's microVM.
type agentRequest struct {
	// Op is "hello" or "run".
	Op string `json:"op"`
	// For hello: the guest's address on its network, and the time.
	IP      string `json:"ip,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Time    int64  `json:"time,omitempty"`
	// For run.
	Argv      []string `json:"argv,omitempty"`
	Env       []string `json:"env,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	BodyLen   int64    `json:"body_len,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

// agentResult is how the agent says a request ended.
type agentResult struct {
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
	OOM      bool   `json:"oom,omitempty"`
}

// ask sends the agent one request and reads its answer.
func (c *functionCopy) ask(ctx context.Context, req agentRequest, body io.Reader, bodyLen int64, stderr func(string), maxOutput int64, limit time.Duration) (FunctionResult, error) {
	var res FunctionResult
	conn, err := dialVsock(filepath.Join(c.dir, functionVsock), guestAgentPort)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(limit))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	header, _ := json.Marshal(req)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(header)))
	if _, err := conn.Write(append(size[:], header...)); err != nil {
		return res, err
	}
	if body != nil && bodyLen > 0 {
		// Sent while the answer is read: a program may write before it has
		// read everything it was sent.
		go io.CopyN(conn, body, bodyLen)
	}

	r := bufio.NewReader(conn)
	var out, errLine bytes.Buffer
	for {
		var head [5]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("the function's microVM stopped answering: %w", err)
		}
		n := int64(binary.BigEndian.Uint32(head[1:]))
		switch head[0] {
		case frameStdout:
			if maxOutput > 0 && int64(out.Len())+n > maxOutput {
				// Closing the connection has the agent stop the program.
				res.TooBig = true
				return res, nil
			}
			if _, err := io.CopyN(&out, r, n); err != nil {
				return res, err
			}
		case frameStderr:
			if _, err := io.CopyN(&errLine, r, n); err != nil {
				return res, err
			}
			for {
				line, rest, found := bytes.Cut(errLine.Bytes(), []byte("\n"))
				if !found {
					break
				}
				if stderr != nil {
					stderr(strings.TrimRight(string(line), "\r"))
				}
				errLine = *bytes.NewBuffer(append([]byte(nil), rest...))
			}
		case frameResult:
			var ar agentResult
			if err := json.NewDecoder(io.LimitReader(r, n)).Decode(&ar); err != nil {
				return res, err
			}
			if errLine.Len() > 0 && stderr != nil {
				stderr(errLine.String())
			}
			if ar.Error != "" {
				return res, errors.New(ar.Error)
			}
			res.Output, res.ExitCode, res.Signal = out.Bytes(), ar.ExitCode, ar.Signal
			res.TimedOut, res.OutOfMemory = ar.TimedOut, ar.OOM
			return res, nil
		default:
			return res, fmt.Errorf("the function's microVM sent something Pail doesn't understand")
		}
	}
}

// dialVsock reaches a port inside a guest through Firecracker's socket.
func dialVsock(socket string, port int) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, err
	}
	// The answer is read a byte at a time: what follows it is the guest's.
	var line []byte
	for one := make([]byte, 1); len(line) < 64; {
		if _, err := conn.Read(one); err != nil {
			conn.Close()
			return nil, fmt.Errorf("nothing in the guest is listening yet: %w", err)
		}
		if one[0] == '\n' {
			break
		}
		line = append(line, one[0])
	}
	if !strings.HasPrefix(string(line), "OK ") {
		conn.Close()
		return nil, fmt.Errorf("the guest refused the connection: %s", line)
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// fcAPI sends one order to a Firecracker's API socket, waiting a moment for
// the socket to be there.
func fcAPI(ctx context.Context, socket, method, path string, body any) error {
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}
	defer client.CloseIdleConnections()
	raw, _ := json.Marshal(body)
	var last error
	for attempt := 0; attempt < 100; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// Firecracker makes its socket a moment after it starts.
			last = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
			continue
		}
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("firecracker refused %s %s: %s", method, path, strings.TrimSpace(string(answer)))
		}
		return nil
	}
	return fmt.Errorf("firecracker's API never came up: %w", last)
}

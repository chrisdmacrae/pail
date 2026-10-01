//go:build linux

package microvm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Job is one command to run in a throwaway microVM.
type Job struct {
	// Image is the root filesystem. It is mounted read-only.
	Image *Image
	// Stage is a folder on this machine whose contents become the guest's
	// /work, the one place a job can write that outlasts it.
	Stage string
	// WorkMB is how big /work is. It only takes the space it uses.
	WorkMB int64
	Argv   []string
	// Env is added to the image's own environment.
	Env []string
	// Dir is where the command runs, inside the guest.
	Dir     string
	VCPUs   int
	MemMB   int
	Network bool
	// Log receives the job's output a line at a time.
	Log func(line string)
}

// Finished is a job that ran to its end.
type Finished struct {
	ExitCode int
	// Work is the work disk, to read the job's files from with Extract.
	Work string
}

// ErrNoExit means the microVM stopped without the job reporting how it
// ended: it crashed, ran out of memory, or was killed.
var ErrNoExit = errors.New("the microVM stopped before the job finished")

// drive is one disk of a microVM. The first is its root.
type drive struct {
	id, path string
	readOnly bool
}

// bootSpec is everything Firecracker is told about one microVM.
type bootSpec struct {
	// dir holds the microVM's config and Firecracker's own log.
	dir    string
	drives []drive
	// args are added to the kernel's command line.
	args  string
	vcpus int
	memMB int
	// tap is the microVM's network, or nil for none.
	tap *tap
	// log receives the guest's console a line at a time.
	log func(line string)
	// tied makes the microVM die with Pail rather than outlive it.
	tied bool
	// bareNet leaves the guest's address for something inside it to set,
	// rather than the kernel as it boots.
	bareNet bool
	// vsock, when set, is the socket (in dir) that reaches the guest.
	vsock string
	// api, when set, is the socket (in dir) Firecracker takes orders on.
	api string
	// restore, when set, is a snapshot to resume instead of booting.
	restore *snapshot
}

// snapshot is a paused microVM on disk.
type snapshot struct {
	mem, state string
}

// booted is a microVM that was started.
type booted struct {
	// done closes when Firecracker has exited and the console is drained.
	done chan struct{}
	// What follows is safe to read once done is closed.
	waitErr  error
	problems strings.Builder
}

// boot starts Firecracker. Cancelling ctx kills the microVM.
func (r *Runner) boot(ctx context.Context, b bootSpec) (*booted, error) {
	root := b.drives[0]
	mode := "ro"
	if !root.readOnly {
		mode = "rw"
	}
	bootArgs := "console=ttyS0 reboot=k panic=1 quiet loglevel=1 root=/dev/vda " + mode + " init=" + guestInit
	var drives []map[string]any
	for i, d := range b.drives {
		drives = append(drives, map[string]any{"drive_id": d.id, "path_on_host": d.path, "is_root_device": i == 0, "is_read_only": d.readOnly})
	}
	config := map[string]any{
		"drives":         drives,
		"machine-config": map[string]any{"vcpu_count": b.vcpus, "mem_size_mib": b.memMB},
	}
	if b.tap != nil {
		if !b.bareNet {
			bootArgs += " " + b.tap.bootParam
		}
		config["network-interfaces"] = []map[string]any{{"iface_id": "eth0", "guest_mac": b.tap.GuestMAC, "host_dev_name": b.tap.Name}}
	}
	if b.args != "" {
		bootArgs += " " + b.args
	}
	if b.vsock != "" {
		config["vsock"] = map[string]any{"guest_cid": 3, "uds_path": b.vsock}
	}
	config["boot-source"] = map[string]any{"kernel_image_path": r.cfg.Kernel, "boot_args": bootArgs}
	configFile := filepath.Join(b.dir, "vm.json")
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(configFile, raw, 0o644); err != nil {
		return nil, err
	}

	// Firecracker's own log goes to a file, leaving its stdout to the guest.
	// It wants the file to be there already.
	if err := os.WriteFile(filepath.Join(b.dir, "firecracker.log"), nil, 0o644); err != nil {
		return nil, err
	}
	args := []string{"--log-path", filepath.Join(b.dir, "firecracker.log"), "--level", "Warning"}
	if b.api != "" {
		args = append(args, "--api-sock", b.api)
	} else {
		args = append(args, "--no-api")
	}
	if b.restore == nil {
		args = append(args, "--config-file", configFile)
	}
	cmd := exec.CommandContext(ctx, r.cfg.Firecracker, args...)
	cmd.Dir = b.dir
	// A group of its own, so killing it can't reach Pail.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if b.tied {
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	}
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	vm := &booted{done: make(chan struct{})}
	cmd.Stderr = &vm.problems
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("can't start firecracker: %w", err)
	}
	go func() {
		// The guest's serial console is Firecracker's stdout.
		console := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := console.ReadString('\n')
			if line = cleanLine(line); line != "" && b.log != nil {
				b.log(line)
			}
			if err != nil {
				break
			}
		}
		vm.waitErr = cmd.Wait()
		close(vm.done)
	}()
	if b.restore != nil {
		load := map[string]any{
			"snapshot_path": b.restore.state,
			"mem_backend":   map[string]any{"backend_type": "File", "backend_path": b.restore.mem},
			"resume_vm":     true,
		}
		if b.tap != nil {
			// The snapshot remembers the tap it was taken with; this copy
			// has one of its own.
			load["network_overrides"] = []map[string]any{{"iface_id": "eth0", "host_dev_name": b.tap.Name}}
		}
		if err := fcAPI(ctx, filepath.Join(b.dir, b.api), "PUT", "/snapshot/load", load); err != nil {
			cmd.Process.Kill()
			<-vm.done
			return nil, fmt.Errorf("restore the snapshot: %w", err)
		}
	}
	return vm, nil
}

// Run boots a microVM, runs the job in it, and waits for it to power off.
// Cancelling ctx kills the microVM.
func (r *Runner) Run(ctx context.Context, job Job) (Finished, error) {
	dir := filepath.Dir(job.Stage)
	if err := os.MkdirAll(filepath.Join(job.Stage, ".pail"), 0o755); err != nil {
		return Finished{}, err
	}
	spec, _ := json.Marshal(guestJobSpec{Argv: job.Argv, Env: append(append([]string{}, job.Image.Env...), job.Env...), Dir: job.Dir})
	if err := os.WriteFile(filepath.Join(job.Stage, ".pail", "job"), spec, 0o644); err != nil {
		return Finished{}, err
	}
	work := filepath.Join(dir, "work.ext4")
	if err := makeExt4(ctx, job.Stage, work, job.WorkMB, 0); err != nil {
		return Finished{}, err
	}

	b := bootSpec{
		dir:    dir,
		drives: []drive{{"rootfs", job.Image.Path, true}, {"work", work, false}},
		vcpus:  job.VCPUs, memMB: job.MemMB, log: job.Log,
	}
	if job.Network {
		t, err := r.net.acquire()
		if err != nil {
			return Finished{}, fmt.Errorf("can't set up the microVM's network: %w", err)
		}
		defer t.release()
		b.tap = t
	}
	vm, err := r.boot(ctx, b)
	if err != nil {
		return Finished{}, err
	}
	<-vm.done
	if ctx.Err() != nil {
		return Finished{}, ctx.Err()
	}

	code, err := r.read(work, "/.pail/exit")
	if err != nil || code == "" {
		if vm.waitErr != nil && vm.problems.Len() > 0 {
			return Finished{}, fmt.Errorf("%w: %s", ErrNoExit, strings.TrimSpace(vm.problems.String()))
		}
		return Finished{}, ErrNoExit
	}
	n, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return Finished{}, ErrNoExit
	}
	return Finished{ExitCode: n, Work: work}, nil
}

var (
	// Colours and cursor moves a tool prints for a terminal.
	ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	// The guest kernel's own messages, such as the one it powers off with.
	kernelLine = regexp.MustCompile(`^\[ *\d+\.\d+\] `)
)

// cleanLine makes a line from the guest's console fit for a deploy log, or
// returns "" for one that isn't the job's.
func cleanLine(line string) string {
	line = ansi.ReplaceAllString(strings.TrimRight(line, "\r\n"), "")
	if kernelLine.MatchString(line) {
		return ""
	}
	return strings.TrimRight(line, " ")
}

// read returns a small file from a work disk, or "" if it isn't there.
func (r *Runner) read(disk, path string) (string, error) {
	out, err := exec.Command("debugfs", "-R", "cat "+path, disk).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// dump copies one file out of a work disk to file on this machine. path is
// as the guest saw it under /work.
func (r *Runner) dump(disk, path, file string) error {
	out, err := exec.Command("debugfs", "-R", fmt.Sprintf("dump %q %q", path, file), disk).CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if info, err := os.Stat(file); err != nil || info.Size() == 0 {
		return fmt.Errorf("the microVM left no %s", path)
	}
	return nil
}

// Extract copies a folder out of a work disk into dir on this machine,
// without mounting anything. path is as the guest saw it under /work.
func (r *Runner) Extract(disk, path, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out, err := exec.Command("debugfs", "-R", fmt.Sprintf("rdump %q %q", path, dir), disk).CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

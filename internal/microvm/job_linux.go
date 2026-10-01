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
	if err := makeExt4(ctx, job.Stage, work, job.WorkMB); err != nil {
		return Finished{}, err
	}

	bootArgs := "console=ttyS0 reboot=k panic=1 quiet loglevel=1 root=/dev/vda ro init=" + guestInit
	config := map[string]any{
		"boot-source": map[string]any{"kernel_image_path": r.cfg.Kernel},
		"drives": []map[string]any{
			{"drive_id": "rootfs", "path_on_host": job.Image.Path, "is_root_device": true, "is_read_only": true},
			{"drive_id": "work", "path_on_host": work, "is_root_device": false, "is_read_only": false},
		},
		"machine-config": map[string]any{"vcpu_count": job.VCPUs, "mem_size_mib": job.MemMB},
	}
	if job.Network {
		t, err := r.net.acquire()
		if err != nil {
			return Finished{}, fmt.Errorf("can't set up the microVM's network: %w", err)
		}
		defer t.release()
		bootArgs += " " + t.bootParam
		config["network-interfaces"] = []map[string]any{{"iface_id": "eth0", "guest_mac": t.GuestMAC, "host_dev_name": t.Name}}
	}
	config["boot-source"].(map[string]any)["boot_args"] = bootArgs
	configFile := filepath.Join(dir, "vm.json")
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(configFile, raw, 0o644); err != nil {
		return Finished{}, err
	}

	// Firecracker's own log goes to a file, leaving its stdout to the guest.
	// It wants the file to be there already.
	if err := os.WriteFile(filepath.Join(dir, "firecracker.log"), nil, 0o644); err != nil {
		return Finished{}, err
	}
	cmd := exec.CommandContext(ctx, r.cfg.Firecracker, "--no-api", "--config-file", configFile,
		"--log-path", filepath.Join(dir, "firecracker.log"), "--level", "Warning")
	cmd.Dir = dir
	// A group of its own, so killing it can't reach Pail.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Finished{}, err
	}
	var problems strings.Builder
	cmd.Stderr = &problems
	if err := cmd.Start(); err != nil {
		return Finished{}, fmt.Errorf("can't start firecracker: %w", err)
	}
	// The guest's serial console is Firecracker's stdout.
	console := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := console.ReadString('\n')
		if line = cleanLine(line); line != "" && job.Log != nil {
			job.Log(line)
		}
		if err != nil {
			break
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return Finished{}, ctx.Err()
	}

	code, err := r.read(work, "/.pail/exit")
	if err != nil || code == "" {
		if waitErr != nil && problems.Len() > 0 {
			return Finished{}, fmt.Errorf("%w: %s", ErrNoExit, strings.TrimSpace(problems.String()))
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

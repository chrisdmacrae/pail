//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How long a container has to finish after it's asked to stop.
const stopGrace = 10 * time.Second

// machine is a long-lived microVM.
type machine struct {
	tap    *tap
	vm     *booted
	cancel context.CancelFunc
	gone   chan struct{}
	stop   sync.Once
}

// Start boots a long-lived microVM from a root filesystem. It returns as
// soon as Firecracker is running; what's inside takes a moment longer.
func (r *Runner) Start(ctx context.Context, spec MachineSpec) (Machine, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("nothing to run")
	}
	os.RemoveAll(spec.Dir)
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return nil, err
	}
	fail := func(err error) (Machine, error) {
		os.RemoveAll(spec.Dir)
		return nil, err
	}

	// The machine gets a root filesystem of its own to write to, with what
	// it should run left inside for init to find.
	root := filepath.Join(spec.Dir, "root.ext4")
	if out, err := exec.CommandContext(ctx, "cp", "--sparse=always", "--reflink=auto", spec.Rootfs, root).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("copy the root filesystem: %v: %s", err, strings.TrimSpace(string(out))))
	}
	job, _ := json.Marshal(guestJobSpec{
		Argv: spec.Argv, Env: spec.Env, Dir: spec.WorkingDir,
		User: spec.User, Hostname: spec.Hostname, Data: spec.DataPath,
	})
	jobFile := filepath.Join(spec.Dir, "job")
	if err := os.WriteFile(jobFile, job, 0o600); err != nil {
		return fail(err)
	}
	out, err := exec.CommandContext(ctx, "debugfs", "-w", "-R", fmt.Sprintf("write %q %q", jobFile, guestServiceJob), root).CombinedOutput()
	if err != nil {
		return fail(fmt.Errorf("debugfs: %v: %s", err, strings.TrimSpace(string(out))))
	}
	os.Remove(jobFile) // it may hold secrets from pail.json's env
	if got, _ := r.read(root, guestServiceJob); got != string(job) {
		return fail(fmt.Errorf("can't write to the root filesystem: %s", strings.TrimSpace(string(out))))
	}

	drives := []drive{{"rootfs", root, false}}
	if spec.Data != "" {
		if err := makeVolume(ctx, spec.Data, spec.DataMB); err != nil {
			return fail(err)
		}
		drives = append(drives, drive{"data", spec.Data, false})
	}

	t, err := r.net.acquire()
	if err != nil {
		return fail(fmt.Errorf("can't set up the microVM's network: %w", err))
	}
	// The machine outlives the call that started it.
	run, cancel := context.WithCancel(context.Background())
	vm, err := r.boot(run, bootSpec{
		dir: spec.Dir, drives: drives, args: guestModeArg + "=" + guestModeService,
		vcpus: spec.VCPUs, memMB: spec.MemMB, tap: t, log: spec.Log, tied: true,
	})
	if err != nil {
		cancel()
		t.release()
		return fail(err)
	}
	m := &machine{tap: t, vm: vm, cancel: cancel, gone: make(chan struct{})}
	go func() {
		<-vm.done
		cancel()
		t.release()
		os.RemoveAll(spec.Dir)
		close(m.gone)
	}()
	return m, nil
}

func (m *machine) Addr(port int) string {
	return net.JoinHostPort(m.tap.GuestIP, strconv.Itoa(port))
}

func (m *machine) Done() <-chan struct{} { return m.gone }

func (m *machine) Stop() {
	m.stop.Do(func() {
		// Init inside listens for this. It passes it on to what it runs,
		// and powers the machine off when that has finished.
		if conn, err := net.DialTimeout("tcp", m.Addr(guestControlPort), 2*time.Second); err == nil {
			conn.Write([]byte("stop\n"))
			conn.Close()
			select {
			case <-m.gone:
				return
			case <-time.After(stopGrace + 5*time.Second):
			}
		}
		m.cancel()
	})
	<-m.gone
}

// makeVolume makes an empty ext4 volume, unless it is already there.
func makeVolume(ctx context.Context, file string, sizeMB int64) error {
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	empty, err := os.MkdirTemp(filepath.Dir(file), ".empty-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(empty)
	// Made beside itself and moved into place, so a half-made volume is
	// never mistaken for one with someone's data in it.
	if err := makeExt4(ctx, empty, file+".tmp", sizeMB, 0); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}

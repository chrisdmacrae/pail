//go:build linux && kvm

// These tests boot real microVMs, so they need a Linux host with KVM and
// root. Run them with: make test-kvm

package microvm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The root filesystems these tests make boot this very test binary as init.
func TestMain(m *testing.M) {
	if IsGuestInit() {
		GuestMain()
		return
	}
	os.Exit(m.Run())
}

const testImage = "docker.io/library/alpine:3"

func testRunner(t *testing.T) *Runner {
	t.Helper()
	scratch := os.Getenv("PAIL_KVM_SCRATCH")
	if scratch == "" {
		scratch = "/var/lib/pail-dev/test"
	}
	r := New(Config{Firecracker: os.Getenv("PAIL_FIRECRACKER"), Kernel: os.Getenv("PAIL_KERNEL"), Dir: scratch}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if ok, why := r.Available(); !ok {
		t.Fatalf("this host can't run microVMs: %s", why)
	}
	return r
}

// lines collects a job's output.
type lines struct {
	mu  sync.Mutex
	all []string
}

func (l *lines) add(s string) { l.mu.Lock(); l.all = append(l.all, s); l.mu.Unlock() }
func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.all, "\n")
}

// sh runs a shell script in a throwaway microVM.
func sh(t *testing.T, r *Runner, network bool, script string) (Finished, *lines, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out := &lines{}
	image, err := r.Image(ctx, testImage, out.add)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	done, err := r.Run(ctx, Job{
		Image: image, Stage: filepath.Join(dir, "stage"), WorkMB: 256,
		Argv: []string{"/bin/sh", "-c", script}, Dir: "/", VCPUs: 1, MemMB: 256, Network: network, Log: out.add,
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	return done, out, dir
}

func TestJobRunsAndHandsBackFiles(t *testing.T) {
	r := testRunner(t)
	start := time.Now()
	done, out, dir := sh(t, r, false, `echo "hello from $(uname -s) as pid $$"; mkdir -p /work/out/deep; echo result > /work/out/deep/file.txt; touch /cant-write-root 2>/dev/null && echo ROOT-WRITABLE; echo "PATH=$PATH"`)
	t.Logf("a microVM job took %s", time.Since(start).Round(time.Millisecond))
	if done.ExitCode != 0 {
		t.Fatalf("exit %d\n%s", done.ExitCode, out)
	}
	got := out.String()
	if !strings.Contains(got, "hello from Linux") {
		t.Errorf("the job's output didn't arrive:\n%s", got)
	}
	if strings.Contains(got, "ROOT-WRITABLE") {
		t.Error("the shared root filesystem was writable")
	}
	// The image's own environment reaches the job.
	if !strings.Contains(got, "PATH=/usr/local/sbin") {
		t.Errorf("the image's PATH didn't reach the job:\n%s", got)
	}
	if err := r.Extract(done.Work, "/out", filepath.Join(dir, "got")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "got", "out", "deep", "file.txt")); err != nil || string(b) != "result\n" {
		t.Errorf("the file the job wrote: %q, %v", b, err)
	}
}

// A package manager can put gigabytes in /tmp. It has to land on the work
// disk: in memory it would fill a small machine, as yarn's cache once did.
func TestTmpIsOnDiskNotInMemory(t *testing.T) {
	r := testRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out := &lines{}
	image, err := r.Image(ctx, testImage, out.add)
	if err != nil {
		t.Fatal(err)
	}
	// Twice the machine's memory, written to /tmp.
	done, err := r.Run(ctx, Job{
		Image: image, Stage: filepath.Join(t.TempDir(), "stage"), WorkMB: 1024,
		Argv: []string{"/bin/sh", "-c", "dd if=/dev/zero of=/tmp/big bs=1M count=512 2>&1 | tail -1; ls -ld /tmp; touch /tmp/as-anyone"},
		Dir:  "/", VCPUs: 1, MemMB: 256, Log: out.add,
	})
	if err != nil || done.ExitCode != 0 {
		t.Fatalf("writing 512MB to /tmp in a 256MB machine: exit %d, %v\n%s", done.ExitCode, err, out)
	}
	if !strings.Contains(out.String(), "drwxrwxrwt") {
		t.Errorf("/tmp isn't world-writable and sticky:\n%s", out)
	}
}

func TestJobExitCodeAndCancel(t *testing.T) {
	r := testRunner(t)
	if done, out, _ := sh(t, r, false, "echo failing; exit 7"); done.ExitCode != 7 {
		t.Errorf("exit code %d, want 7\n%s", done.ExitCode, out)
	}

	// A job that would run for ever is killed when its context ends.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	image, _ := r.Image(ctx, testImage, func(string) {})
	start := time.Now()
	_, err := r.Run(ctx, Job{Image: image, Stage: filepath.Join(t.TempDir(), "stage"), WorkMB: 64, Argv: []string{"/bin/sleep", "600"}, Dir: "/", VCPUs: 1, MemMB: 128})
	if err == nil || time.Since(start) > 20*time.Second {
		t.Errorf("a cancelled job: err=%v after %s", err, time.Since(start))
	}
	if out, _ := exec.Command("pgrep", "-f", "firecracker --no-api").Output(); len(out) != 0 {
		t.Errorf("firecracker is still running after the job was cancelled: %s", out)
	}
}

func TestGuestReachesTheInternetAndNothingElse(t *testing.T) {
	r := testRunner(t)

	// Something on this machine, and something on its network, for the
	// guest to fail to reach.
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	gateway := ""
	if out, err := exec.Command("sh", "-c", "ip route show default | awk '{print $3; exit}'").Output(); err == nil {
		gateway = strings.TrimSpace(string(out))
	}
	lan := ""
	if out, err := exec.Command("sh", "-c", "ip -4 route get 1.1.1.1 | grep -o 'src [0-9.]*' | cut -d' ' -f2").Output(); err == nil {
		lan = strings.TrimSpace(string(out))
	}
	if gateway == "" || lan == "" {
		t.Fatalf("couldn't find this machine's network: gateway %q, address %q", gateway, lan)
	}

	script := fmt.Sprintf(`
gw=$(ip route | awk '/default/ {print $3}')
echo "guest gateway: $gw"
wget -q -T 15 -O /work/page http://example.com && echo INTERNET-OK
nc -w 3 $gw %[1]d </dev/null && echo REACHED-HOST-BY-TAP
nc -w 3 %[2]s %[1]d </dev/null && echo REACHED-HOST-BY-LAN-ADDRESS
nc -w 3 %[3]s 53 </dev/null && echo REACHED-HOME-NETWORK
nc -w 3 172.30.0.1 %[1]d </dev/null && echo REACHED-ANOTHER-GUEST-NET
true`, port, lan, gateway)
	done, out, _ := sh(t, r, true, script)
	got := out.String()
	t.Logf("guest saw:\n%s", got)
	if done.ExitCode != 0 || !strings.Contains(got, "INTERNET-OK") {
		t.Errorf("the guest couldn't reach the internet")
	}
	for _, bad := range []string{"REACHED-HOST-BY-TAP", "REACHED-HOST-BY-LAN-ADDRESS", "REACHED-HOME-NETWORK"} {
		if strings.Contains(got, bad) {
			t.Errorf("isolation is broken: %s", bad)
		}
	}
	// The check means something only if this machine can reach those itself.
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", lan, port), 2*time.Second); err != nil {
		t.Errorf("the test's own listener isn't reachable from this machine: %v", err)
	} else {
		c.Close()
	}

	// The tap device is gone once the job is.
	if out, _ := exec.Command("sh", "-c", "ip -o link show | grep -c pailvm").Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("tap devices left behind: %s", out)
	}
}

func TestBuildSite(t *testing.T) {
	r := testRunner(t)
	out := &lines{}
	start := time.Now()
	result, err := r.BuildSite(context.Background(), BuildRequest{
		Log: out.add,
		Fill: func(dir string) error {
			// A project with a real dependency, so the build has to reach npm.
			files := map[string]string{
				"package.json": `{"name": "site", "scripts": {"build": "node build.js"}, "dependencies": {"is-odd": "3.0.1"}}`,
				"build.js":     `const fs = require("fs"); fs.mkdirSync("dist"); fs.writeFileSync("dist/index.html", "<h1>3 is odd: " + require("is-odd")(3) + "</h1>");`,
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BuildSite: %v\n%s", err, out)
	}
	defer result.Cleanup()
	t.Logf("the build took %s and logged:\n%s", time.Since(start).Round(time.Millisecond), out)
	if result.Output != "dist" {
		t.Errorf("output folder %q, want dist", result.Output)
	}
	if b, err := os.ReadFile(filepath.Join(result.Dir, "index.html")); err != nil || string(b) != "<h1>3 is odd: true</h1>" {
		t.Errorf("built page: %q, %v", b, err)
	}

	// A build that fails says so, and one with nowhere to find the site too.
	_, err = r.BuildSite(context.Background(), BuildRequest{Fill: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts": {"build": "echo nope >&2; exit 3"}}`), 0o644)
	}})
	if err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Errorf("a failing build: %v", err)
	}
	_, err = r.BuildSite(context.Background(), BuildRequest{Fill: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts": {"build": "mkdir www && echo x > www/index.html"}}`), 0o644)
	}})
	if err == nil || !strings.Contains(err.Error(), "found no index.html") {
		t.Errorf("a build with its site somewhere unusual: %v", err)
	}
}

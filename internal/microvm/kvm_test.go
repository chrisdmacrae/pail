//go:build linux && kvm

// These tests boot real microVMs, so they need a Linux host with KVM and
// root. Run them with: make test-kvm

package microvm

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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

// A Dockerfile app that answers on a port, keeps a count on its data volume,
// and says goodbye when it's asked to stop.
var testApp = map[string]string{
	// A short name, as Dockerfiles have: it means Docker Hub's.
	"Dockerfile": `FROM alpine:3
RUN apk add --no-cache busybox-extras && adduser -D -u 1234 app
COPY app/ /srv/
RUN chmod +x /srv/run.sh /srv/www/cgi-bin/info
ENV GREETING=hello
WORKDIR /srv
USER app
CMD ["./run.sh"]
`,
	"app/run.sh": `#!/bin/sh
trap 'echo "goodbye from the app"; exit 0' TERM
echo "starting as $(id -un) in $(pwd), greeting $GREETING, port $PORT"
n=$(cat /data/boots 2>/dev/null || echo 0); n=$((n + 1)); echo $n > /data/boots
echo "boot $n"
touch /tmp/scratch && echo "root is writable"
httpd -f -p "$PORT" -h /srv/www &
wait $!
`,
	"app/www/index.html": "the app's page\n",
	"app/www/cgi-bin/info": `#!/bin/sh
printf 'Content-Type: text/plain\r\n\r\n'
echo "boots=$(cat /data/boots) host=$(hostname)"
`,
}

func fillWith(files map[string]string) func(string) error {
	return func(dir string) error {
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// fetch asks a machine for a page, waiting for its port to open.
func fetch(t *testing.T, m Machine, port int, path string) string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		resp, err := http.Get("http://" + m.Addr(port) + path)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return string(body)
		}
		select {
		case <-m.Done():
			t.Fatalf("the machine stopped before it answered on %d", port)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing answered on %d: %v", port, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestContainerBuildsRunsAndStops(t *testing.T) {
	r := testRunner(t)
	out := &lines{}
	start := time.Now()
	built, err := r.BuildContainer(context.Background(), ContainerBuild{Fill: fillWith(testApp), Dockerfile: "Dockerfile", Context: ".", Log: out.add})
	if err != nil {
		t.Fatalf("BuildContainer: %v\n%s", err, out)
	}
	defer built.Cleanup()
	t.Logf("the build took %s and logged:\n%s", time.Since(start).Round(time.Millisecond), out)
	img := built.Image
	if len(img.Cmd) != 1 || img.Cmd[0] != "./run.sh" || img.User != "app" || img.WorkingDir != "/srv" {
		t.Fatalf("what the image says about running it: %+v", img)
	}

	dir := t.TempDir()
	volume := filepath.Join(dir, "volume.ext4")
	spec := MachineSpec{
		Dir: filepath.Join(dir, "run"), Rootfs: img.Path,
		Data: volume, DataPath: "/data", DataMB: 64,
		Argv: append(append([]string{}, img.Entrypoint...), img.Cmd...),
		Env:  append(append([]string{}, img.Env...), "PORT=8080"), WorkingDir: img.WorkingDir, User: img.User,
		Hostname: "api", VCPUs: 1, MemMB: 128,
	}
	boot := func() (Machine, *lines) {
		said := &lines{}
		spec.Log = said.add
		m, err := r.Start(context.Background(), spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return m, said
	}

	m, said := boot()
	start = time.Now()
	if got := fetch(t, m, 8080, "/"); got != "the app's page\n" {
		t.Errorf("the app's page: %q", got)
	}
	t.Logf("the app answered %s after its machine started", time.Since(start).Round(time.Millisecond))
	if got := fetch(t, m, 8080, "/cgi-bin/info"); got != "boots=1 host=api\n" {
		t.Errorf("the first boot: %q", got)
	}
	start = time.Now()
	m.Stop()
	t.Logf("stopping took %s; the machine said:\n%s", time.Since(start).Round(time.Millisecond), said)
	for _, want := range []string{"starting as app in /srv, greeting hello, port 8080", "root is writable", "goodbye from the app"} {
		if !strings.Contains(said.String(), want) {
			t.Errorf("the machine's output is missing %q", want)
		}
	}
	if _, err := os.Stat(spec.Dir); !os.IsNotExist(err) {
		t.Errorf("the machine's folder outlived it: %v", err)
	}

	// The data volume outlasts the machine; the root filesystem doesn't.
	m, said = boot()
	if got := fetch(t, m, 8080, "/cgi-bin/info"); got != "boots=2 host=api\n" {
		t.Errorf("the second boot: %q\n%s", got, said)
	}
	m.Stop()

	// A machine whose app exits stops by itself and says how.
	spec.Argv, spec.Data, spec.DataPath = []string{"/bin/sh", "-c", "echo bye; exit 3"}, "", ""
	m, said = boot()
	select {
	case <-m.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("a machine whose app exited is still running")
	}
	if !strings.Contains(said.String(), "exited with status 3") {
		t.Errorf("how the app ended:\n%s", said)
	}
	if out, _ := exec.Command("sh", "-c", "ip -o link show | grep -c pailvm").Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("tap devices left behind: %s", out)
	}
}

// An image can be made by anyone. However its links are laid out, unpacking
// it must leave everything outside its own folder alone.
func TestUnpackingAnImageStaysInsideItsFolder(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	os.WriteFile(victim, []byte("untouched"), 0o600)
	os.Mkdir(filepath.Join(outside, "dir"), 0o700)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o777
		}
		tw.WriteHeader(&h)
		tw.Write([]byte(body))
	}
	// Links out of the image: to a file, to a folder, absolute and relative.
	add(tar.Header{Name: "file-link", Typeflag: tar.TypeSymlink, Linkname: victim}, "")
	add(tar.Header{Name: "dir-link", Typeflag: tar.TypeSymlink, Linkname: outside}, "")
	add(tar.Header{Name: "up-link", Typeflag: tar.TypeSymlink, Linkname: "../../../../../../../../../../" + outside}, "")
	// Then entries that go through them.
	add(tar.Header{Name: "dir-link/dropped", Typeflag: tar.TypeReg}, "through an absolute link")
	add(tar.Header{Name: "up-link/dropped-too", Typeflag: tar.TypeReg}, "through a relative link")
	add(tar.Header{Name: "dir-link/dir", Typeflag: tar.TypeDir}, "")
	add(tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "file-link"}, "")
	add(tar.Header{Name: "file-link", Typeflag: tar.TypeReg}, "replaces the link, not what it pointed at")
	// And links where Pail puts its own files.
	add(tar.Header{Name: "pail-init", Typeflag: tar.TypeSymlink, Linkname: filepath.Join(outside, "init")}, "")
	add(tar.Header{Name: "etc", Typeflag: tar.TypeSymlink, Linkname: outside}, "")
	add(tar.Header{Name: "proc", Typeflag: tar.TypeSymlink, Linkname: filepath.Join(outside, "proc")}, "")
	// An honest image's links still work: a merged /usr, as most have.
	add(tar.Header{Name: "usr/lib", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "/usr/lib"}, "")
	add(tar.Header{Name: "lib/libc.so", Typeflag: tar.TypeReg, Mode: 0o644}, "a library")
	tw.Close()

	root := filepath.Join(t.TempDir(), "root")
	if err := untar(&buf, root); err != nil {
		t.Fatalf("untar: %v", err)
	}
	if err := prepareRoot(root); err != nil {
		t.Fatalf("prepareRoot: %v", err)
	}

	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("a file outside the image was written: %q", b)
	}
	if info, _ := os.Stat(victim); info.Mode().Perm() != 0o600 {
		t.Errorf("a file outside the image had its mode changed to %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(outside, "dir")); info.Mode().Perm() != 0o700 {
		t.Errorf("a folder outside the image had its mode changed to %v", info.Mode().Perm())
	}
	left, _ := os.ReadDir(outside)
	if len(left) != 2 {
		names := []string{}
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("unpacking left things outside the image: %v", names)
	}
	if b, err := os.ReadFile(filepath.Join(root, "usr/lib/libc.so")); err != nil || string(b) != "a library" {
		t.Errorf("a file behind an honest link: %q, %v", b, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "pail-init")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("init isn't a real file in the image: %v", err)
	}
}

// An image from a registry boots as it is, with nothing built. This one has
// no shell, no users and no /etc: a single program in an empty filesystem.
func TestContainerFromARegistry(t *testing.T) {
	r := testRunner(t)
	const ref = "traefik/whoami:v1.10"
	out := &lines{}
	start := time.Now()
	built, err := r.PullContainer(context.Background(), ContainerPull{Ref: ref, Log: out.add})
	if err != nil {
		t.Fatalf("PullContainer: %v", err)
	}
	defer built.Cleanup()
	t.Logf("pulling took %s and logged:\n%s", time.Since(start).Round(time.Millisecond), out)
	img := built.Image
	if !strings.HasPrefix(built.Digest, "sha256:") || len(img.Entrypoint)+len(img.Cmd) == 0 {
		t.Fatalf("what came back: digest %q, image %+v", built.Digest, img)
	}

	// Asked again with the digest it has, nothing is fetched.
	same, err := r.PullContainer(context.Background(), ContainerPull{Ref: ref, Unless: built.Digest})
	if err != nil || same.Image != nil || same.Digest != built.Digest {
		t.Errorf("pulling what we already have: %+v, %v", same, err)
	}
	if _, err := r.PullContainer(context.Background(), ContainerPull{Ref: "traefik/whoami:no-such-tag"}); err == nil || !strings.Contains(err.Error(), "can't pull traefik/whoami:no-such-tag") {
		t.Errorf("an image that isn't there: %v", err)
	}

	said := &lines{}
	m, err := r.Start(context.Background(), MachineSpec{
		Dir: filepath.Join(t.TempDir(), "run"), Rootfs: img.Path,
		Argv: append(append([]string{}, img.Entrypoint...), img.Cmd...), Env: img.Env,
		WorkingDir: img.WorkingDir, User: img.User, Hostname: "whoami", VCPUs: 1, MemMB: 128, Log: said.add,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()
	if got := fetch(t, m, 80, "/"); !strings.Contains(got, "Hostname: whoami") {
		t.Errorf("what the image answered: %q\n%s", got, said)
	}
}

// A pail's containers are a group: each reaches the others by name, and
// nothing outside the group reaches any of them.
func TestMachinesInAGroupReachEachOtherByName(t *testing.T) {
	r := testRunner(t)
	built, err := r.PullContainer(context.Background(), ContainerPull{Ref: testImage})
	if err != nil {
		t.Fatalf("PullContainer: %v", err)
	}
	defer built.Cleanup()

	dir := t.TempDir()
	start := func(name, script string) (Machine, *lines) {
		t.Helper()
		said := &lines{}
		m, err := r.Start(context.Background(), MachineSpec{
			Dir: filepath.Join(dir, name), Rootfs: built.Image.Path,
			Argv:     []string{"/bin/sh", "-c", script},
			Hostname: name, Group: "pair.d1", Peers: []string{"store", "web"},
			VCPUs: 1, MemMB: 128, Log: said.add,
		})
		if err != nil {
			t.Fatalf("Start %s: %v", name, err)
		}
		return m, said
	}
	hears := func(said *lines) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for !strings.Contains(said.String(), "web heard: kept by store") {
			if time.Now().After(deadline) {
				t.Fatalf("web never heard from store. It said:\n%s", said)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	const ask = `until got=$(nc -w 3 store 6000 </dev/null) && [ -n "$got" ]; do sleep 1; done; echo "web heard: $got"; exec sleep 3600`

	// web starts first, and waits for store by its name.
	web, said := start("web", ask)
	defer func() { web.Stop() }()
	store, _ := start("store", `while true; do echo "kept by store" | nc -l -p 6000; done`)
	defer store.Stop()
	hears(said)

	// A guest outside the group can't reach one inside it.
	at, _, _ := net.SplitHostPort(store.Addr(6000))
	_, out, _ := sh(t, r, true, fmt.Sprintf("nc -w 3 %s 6000 </dev/null && echo REACHED-A-GROUP; true", at))
	if strings.Contains(out.String(), "REACHED-A-GROUP") {
		t.Errorf("isolation is broken: a guest outside the group reached store")
	}

	// One that restarts comes back at its address, and finds the others.
	before := web.Addr(80)
	web.Stop()
	web, said = start("web", ask)
	if web.Addr(80) != before {
		t.Errorf("web came back at %s, and was at %s", web.Addr(80), before)
	}
	hears(said)

	// Nothing of the group outlasts it.
	web.Stop()
	store.Stop()
	if out, _ := exec.Command("sh", "-c", "iptables -S PAIL-FWD | grep -c -- '-d 172.30.'").Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("rules left behind: %s", out)
	}
	if out, _ := exec.Command("sh", "-c", "ip -o link show | grep -c pailvm").Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("tap devices left behind: %s", out)
	}
}

// A machine of a pail named in PAIL_ALLOW_LAN reaches the home network, and
// still neither this machine's own services nor another guest.
func TestAMachineAllowedTheLANReachesIt(t *testing.T) {
	r := testRunner(t)
	gateway := ""
	if out, err := exec.Command("sh", "-c", "ip route show default | awk '{print $3; exit}'").Output(); err == nil {
		gateway = strings.TrimSpace(string(out))
	}
	// Something on the home network that answers: the gateway's resolver.
	c, err := net.DialTimeout("tcp", net.JoinHostPort(gateway, "53"), 2*time.Second)
	if err != nil {
		t.Skipf("nothing on this machine's network to reach: %v", err)
	}
	c.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	built, err := r.PullContainer(context.Background(), ContainerPull{Ref: testImage})
	if err != nil {
		t.Fatalf("PullContainer: %v", err)
	}
	defer built.Cleanup()
	other, err := r.Start(context.Background(), MachineSpec{
		Dir: filepath.Join(t.TempDir(), "other"), Rootfs: built.Image.Path,
		Argv: []string{"/bin/sh", "-c", `while true; do echo hello | nc -l -p 6000; done`}, Hostname: "other", VCPUs: 1, MemMB: 128,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer other.Stop()
	at, _, _ := net.SplitHostPort(other.Addr(6000))

	script := fmt.Sprintf(`
gw=$(ip route | awk '/default/ {print $3}')
nc -w 3 %[1]s 53 </dev/null && echo REACHED-HOME-NETWORK
nc -w 3 $gw %[2]d </dev/null && echo REACHED-HOST-BY-TAP
nc -w 3 %[3]s 6000 </dev/null && echo REACHED-ANOTHER-GUEST
echo "network filesystems: $(grep -c nfs /proc/filesystems) nfs"
echo DONE; exec sleep 3600`, gateway, port, at)
	try := func(lan bool) string {
		said := &lines{}
		m, err := r.Start(context.Background(), MachineSpec{
			Dir: filepath.Join(t.TempDir(), "run"), Rootfs: built.Image.Path,
			Argv: []string{"/bin/sh", "-c", script}, Hostname: "nas-user", LAN: lan, VCPUs: 1, MemMB: 128, Log: said.add,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer m.Stop()
		deadline := time.Now().Add(90 * time.Second)
		for !strings.Contains(said.String(), "DONE") {
			if time.Now().After(deadline) {
				t.Fatalf("the machine never finished. It said:\n%s", said)
			}
			time.Sleep(250 * time.Millisecond)
		}
		return said.String()
	}

	got := try(true)
	t.Logf("the machine saw:\n%s", got)
	if !strings.Contains(got, "REACHED-HOME-NETWORK") {
		t.Errorf("a machine allowed the home network didn't reach it:\n%s", got)
	}
	for _, bad := range []string{"REACHED-HOST-BY-TAP", "REACHED-ANOTHER-GUEST"} {
		if strings.Contains(got, bad) {
			t.Errorf("isolation is broken: %s", bad)
		}
	}
	// The same machine without the allowance reaches none of it.
	if got := try(false); strings.Contains(got, "REACHED-") {
		t.Errorf("a machine not allowed the home network reached something:\n%s", got)
	}
	other.Stop()
	if out, _ := exec.Command("sh", "-c", "iptables -S PAIL-FWD | grep -c -- '-s 172.30.'").Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("rules left behind: %s", out)
	}
}

// A function: built, snapshotted, then restored as copies that each run a
// program per call.
func TestFunctionSnapshotsAndRuns(t *testing.T) {
	r := testRunner(t)
	out := &lines{}
	dir := t.TempDir()
	start := time.Now()
	fn, err := r.BuildFunction(context.Background(), FunctionBuild{
		Fill: fillWith(map[string]string{"main.sh": `echo "Content-Type: text/plain"
echo
echo "built: $(cat built.txt)"
echo "method: $REQUEST_METHOD greeting: $GREETING"
echo "body: $(cat)"
echo "clock: $(date +%s)"
echo "tmp: $(cat /tmp/kept 2>/dev/null)"; echo "$REQUEST_METHOD" > /tmp/kept
touch /work/src/nope 2>/dev/null && echo CODE-WRITABLE
touch /nope 2>/dev/null && echo ROOT-WRITABLE
wget -q -T 10 -O /dev/null http://example.com && echo "internet: ok"
echo "to the log" >&2
`}),
		BuildImage: testImage, Script: "echo by-the-build > built.txt", RunImage: testImage,
		MemMB: 128, Dir: filepath.Join(dir, "fn"), Log: out.add,
	})
	if err != nil {
		t.Fatalf("BuildFunction: %v\n%s", err, out)
	}
	t.Logf("building and snapshotting took %s and logged:\n%s", time.Since(start).Round(time.Millisecond), out)
	if fn.State == "" {
		t.Fatalf("no snapshot was taken:\n%s", out)
	}

	// Two copies at once, restored from the one snapshot.
	var copies [2]FunctionCopy
	var wg sync.WaitGroup
	for i := range copies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			began := time.Now()
			c, err := r.StartFunction(context.Background(), FunctionSpec{Dir: filepath.Join(dir, fmt.Sprintf("copy%d", i)), Image: fn, MemMB: 128, Log: out.add})
			if err != nil {
				t.Errorf("StartFunction: %v", err)
				return
			}
			t.Logf("copy %d was ready %s after it was asked for", i, time.Since(began).Round(time.Millisecond))
			copies[i] = c
		}()
	}
	wg.Wait()
	if copies[0] == nil || copies[1] == nil {
		t.Fatalf("copies didn't start:\n%s", out)
	}
	defer copies[1].Stop()

	call := func(c FunctionCopy, method, body string, argv ...string) (FunctionResult, string, time.Duration) {
		t.Helper()
		said := &lines{}
		began := time.Now()
		res, err := c.Call(context.Background(), FunctionCall{
			Argv: argv, Dir: "/work/src", Env: append(append([]string{}, fn.Env...), "REQUEST_METHOD="+method, "GREETING=hi"),
			Body: strings.NewReader(body), BodyLen: int64(len(body)), Timeout: 20 * time.Second, MaxOutput: 1 << 20, Stderr: said.add,
		})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		return res, said.String(), time.Since(began)
	}

	for i, c := range copies {
		res, said, took := call(c, "POST", "what was sent", "sh", "main.sh")
		got := string(res.Output)
		t.Logf("copy %d answered in %s:\n%s", i, took.Round(time.Millisecond), got)
		for _, want := range []string{"built: by-the-build", "method: POST greeting: hi", "body: what was sent", "tmp: \n", "internet: ok"} {
			if !strings.Contains(got, want) {
				t.Errorf("copy %d's answer is missing %q", i, want)
			}
		}
		if strings.Contains(got, "WRITABLE") {
			t.Errorf("copy %d could write to a shared disk", i)
		}
		if res.ExitCode != 0 || said != "to the log" {
			t.Errorf("copy %d: exit %d, stderr %q", i, res.ExitCode, said)
		}
		// A restored copy wakes with the clock of the snapshot, and is told the time.
		var clock int64
		if _, after, ok := strings.Cut(got, "clock: "); ok {
			fmt.Sscanf(after, "%d", &clock)
		}
		if drift := time.Now().Unix() - clock; drift < -5 || drift > 5 {
			t.Errorf("copy %d's clock is %ds off", i, drift)
		}
	}
	// A copy stays warm: what one call left in /tmp, the next finds.
	if res, _, took := call(copies[0], "GET", "", "sh", "main.sh"); !strings.Contains(string(res.Output), "tmp: POST") {
		t.Errorf("the second call to a copy: %s", res.Output)
	} else {
		t.Logf("a warm call took %s", took.Round(time.Millisecond))
	}

	// How a call can end.
	if res, _, _ := call(copies[0], "GET", "", "sh", "-c", "echo out; exit 3"); res.ExitCode != 3 || string(res.Output) != "out\n" {
		t.Errorf("a failing program: %+v", res)
	}
	said := &lines{}
	began := time.Now()
	res, err := copies[0].Call(context.Background(), FunctionCall{Argv: []string{"sh", "-c", "sleep 30 & sleep 30"}, Env: fn.Env, Timeout: time.Second, Stderr: said.add})
	if err != nil || !res.TimedOut || time.Since(began) > 5*time.Second {
		t.Errorf("a program that overruns: %+v, %v after %s", res, err, time.Since(began))
	}
	if res, _, _ := call(copies[0], "GET", "", "sh", "-c", "tail /dev/zero"); !res.OutOfMemory {
		t.Errorf("a program that eats all the memory: %+v", res)
	}
	if res, _, _ := call(copies[0], "GET", "", "sh", "-c", "yes | head -c 5000000"); !res.TooBig {
		t.Errorf("a program that writes too much: exit %d, %d bytes, too big %v", res.ExitCode, len(res.Output), res.TooBig)
	}
	// And the copy is still good after all that.
	if res, _, _ := call(copies[0], "GET", "", "sh", "-c", "echo still here; pgrep sleep | wc -l"); string(res.Output) != "still here\n0\n" {
		t.Errorf("after the failures: %q", res.Output)
	}

	copies[0].Stop()
	if _, err := os.Stat(filepath.Join(dir, "copy0")); !os.IsNotExist(err) {
		t.Errorf("a copy's folder outlived it: %v", err)
	}
}

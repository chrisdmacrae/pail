package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/server"
	"github.com/chrisdmacrae/pail/internal/storage"
)

const token = "test-token"

// installation runs a real Pail server on in-memory storage.
func installation(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Config{Token: token, BaseDomain: "pail.lan", MaxUploadSize: 1 << 20, MaxDeploys: 10}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := pails.New(pails.Options{Store: storage.NewMemory(), BaseDomain: cfg.BaseDomain, MaxDeploys: cfg.MaxDeploys, Logger: logger})
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(cfg, svc, logger, "test"))
	t.Cleanup(func() { ts.Close(); svc.Wait() })
	return ts
}

// shell is one person's machine: a home folder, a working folder, an
// environment.
type shell struct {
	t    *testing.T
	home string
	cwd  string
	env  map[string]string
	tty  bool
	// typed is what the person types when asked a question.
	typed string
	// opened collects the URLs handed to the browser.
	opened []string
}

func newShell(t *testing.T) *shell {
	return &shell{t: t, home: t.TempDir(), cwd: t.TempDir(), env: map[string]string{}}
}

func (s *shell) run(args ...string) (code int, stdout, stderr string) {
	s.t.Helper()
	var out, errb bytes.Buffer
	code = Run(Env{
		Args:       args,
		Stdout:     &out,
		Stderr:     &errb,
		Getenv:     func(k string) string { return s.env[k] },
		Home:       s.home,
		Cwd:        s.cwd,
		TTY:        s.tty,
		Stdin:      strings.NewReader(s.typed),
		ReadSecret: func(string) (string, error) { return s.env["typed"], nil },
		OpenURL:    func(u string) error { s.opened = append(s.opened, u); return nil },
		Version:    "test",
	})
	return code, out.String(), errb.String()
}

// ok runs a command that must succeed and returns its stdout.
func (s *shell) ok(args ...string) string {
	s.t.Helper()
	code, out, errOut := s.run(args...)
	if code != ExitOK {
		s.t.Fatalf("pail %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// fails runs a command that must exit with code, and returns its stderr.
func (s *shell) fails(code int, args ...string) string {
	s.t.Helper()
	got, out, errOut := s.run(args...)
	if got != code {
		s.t.Fatalf("pail %s: exit %d, want %d\n%s%s", strings.Join(args, " "), got, code, out, errOut)
	}
	return errOut
}

func (s *shell) write(rel, body string) {
	s.t.Helper()
	p := filepath.Join(s.cwd, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// fetch gets a pail's site through the installation, by Host header.
func fetch(t *testing.T, ts *httptest.Server, host, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func want(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(got, sub) {
			t.Errorf("missing %q in:\n%s", sub, got)
		}
	}
}

func TestLoginUpLsLogs(t *testing.T) {
	ts := installation(t)
	s := newShell(t)
	s.env["PAIL_TOKEN"] = token

	out := s.ok("login", ts.URL, "--profile", "home")
	want(t, out, "as home. It's your default.")
	delete(s.env, "PAIL_TOKEN")

	cfg := filepath.Join(s.home, ".pail", "config")
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(cfg); info == nil || info.Mode().Perm() != 0o600 {
			t.Errorf("config mode: %v", info)
		}
		if info, _ := os.Stat(filepath.Dir(cfg)); info == nil || info.Mode().Perm() != 0o700 {
			t.Errorf("~/.pail mode: %v", info)
		}
	}
	want(t, s.ok("profiles"), "* home", ts.URL)

	want(t, s.ok("ls"), "Nothing in the pail yet.")

	s.write("dist/index.html", "<h1>v1</h1>")
	s.write("dist/node_modules/x/index.js", "not shipped")
	s.write("dist/.DS_Store", "junk")
	// Flags may come after the folder, as in `pail up ./dist -p lab`.
	code, stdout, stderr := s.run("up", "./dist", "--name", "blog", "-p", "home")
	if code != ExitOK {
		t.Fatalf("up: exit %d\n%s", code, stderr)
	}
	port := ts.URL[strings.LastIndex(ts.URL, ":"):]
	if stdout != "http://blog.pail.lan"+port+"\n" {
		t.Errorf("up stdout should be the URL alone, got %q", stdout)
	}
	want(t, stderr, "unpacking 1 file · found index.html", "✓ live at http://blog.pail.lan")
	if got := fetch(t, ts, "blog.pail.lan", "/"); got != "<h1>v1</h1>" {
		t.Errorf("site: %q", got)
	}

	want(t, s.ok("ls"), "NAME", "blog", "Live", "http://blog.pail.lan"+port, "just now")
	if got := s.ok("ls", "--quiet"); got != "http://blog.pail.lan"+port+"\n" {
		t.Errorf("ls --quiet: %q", got)
	}
	want(t, s.ok("ls", "--json"), `"name": "blog"`, `"status": "live"`)

	want(t, s.ok("logs", "blog"), "swapping blog.pail.lan", "✓ live at")
	want(t, s.ok("logs", "blog", "--follow", "--json"), `"level":"ok"`)
	s.fails(ExitNotFound, "logs", "nope")
	s.fails(ExitNotFound, "logs", "blog", "0000000")
}

func TestUpFailures(t *testing.T) {
	ts := installation(t)
	s := newShell(t)
	s.env["PAIL_URL"], s.env["PAIL_TOKEN"] = ts.URL, token

	want(t, s.fails(ExitUsage, "up", "./dist"), "No folder at ./dist.")
	s.write("site/readme.txt", "no site")
	want(t, s.fails(ExitUsage, "up", "./site"), "No index.html in ./site. Point Pail at the folder that has it.")
	s.write("dist/index.html", "v1")
	want(t, s.fails(ExitUsage, "up", "./dist", "--name", "Not_A_Name"), "lowercase letters, numbers and dashes")

	// The server refuses this one after the upload: the deploy fails.
	s.write("fn/pail.json", `{"name": "dash", "functions": {"api": {"src": "./api"}}}`)
	want(t, s.fails(ExitDeployFailed, "up", "./fn"), "functions or containers", "✗ deploy failed")
	want(t, s.fails(ExitDeployFailed, "up", "./fn", "--quiet"), "pail: pail.json declares functions")

	s.write("big/index.html", "x")
	blob := make([]byte, 2<<20)
	rand.New(rand.NewSource(1)).Read(blob) // noise, so gzip can't shrink it
	os.WriteFile(filepath.Join(s.cwd, "big", "blob.bin"), blob, 0o644)
	want(t, s.fails(ExitUsage, "up", "./big", "--name", "big"), "over the 1MB this Pail takes")

	s.env["PAIL_TOKEN"] = "wrong"
	want(t, s.fails(ExitTokenRejected, "up", "./dist", "--name", "blog"), "rejected the token. Check PAIL_TOKEN.")
	s.env["PAIL_URL"] = "http://127.0.0.1:1"
	want(t, s.fails(ExitUnreachable, "ls"), "Can't reach http://127.0.0.1:1")
}

func TestPailName(t *testing.T) {
	ts := installation(t)
	deployed := func(s *shell, args ...string) string {
		t.Helper()
		s.env["PAIL_URL"], s.env["PAIL_TOKEN"] = ts.URL, token
		url := s.ok(append([]string{"up"}, args...)...)
		name, _, _ := strings.Cut(strings.TrimPrefix(url, "http://"), ".")
		return name
	}

	// pail.json's name beats the folders around it.
	s := newShell(t)
	s.write("dist/index.html", "x")
	s.write("dist/pail.json", `{"name": "from-manifest"}`)
	if got := deployed(s, "./dist"); got != "from-manifest" {
		t.Errorf("pail.json name: got %q", got)
	}
	if got := deployed(s, "./dist", "--name", "flagged"); got != "flagged" {
		t.Errorf("--name: got %q", got)
	}

	// Then the git repo's name, wherever inside it pail runs.
	s = newShell(t)
	repo := filepath.Join(s.cwd, "My Garden_Journal")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	s.cwd = filepath.Join(repo, "web")
	s.write("dist/index.html", "x")
	if got := deployed(s, "./dist"); got != "my-garden-journal" {
		t.Errorf("git repo name: got %q", got)
	}

	// Then the current folder, never an output folder like dist.
	s = newShell(t)
	s.cwd = filepath.Join(s.cwd, "recipes", "dist")
	s.write("index.html", "x")
	if got := deployed(s); got != "recipes" {
		t.Errorf("folder name: got %q", got)
	}
}

func TestPickingAnInstallation(t *testing.T) {
	home, lab := installation(t), installation(t)
	s := newShell(t)
	want(t, s.fails(ExitUsage, "ls"), "No Pail installation yet. Run pail login <url>.")

	// With no terminal and no PAIL_TOKEN, login can't ask.
	want(t, s.fails(ExitUsage, "login", home.URL), "Set PAIL_TOKEN")
	s.tty = true
	s.env["typed"] = "wrong"
	want(t, s.fails(ExitTokenRejected, "login", home.URL, "--profile", "home"), "rejected the token")
	s.env["CI"] = "true"
	want(t, s.fails(ExitUsage, "login", home.URL), "Set PAIL_TOKEN")
	delete(s.env, "CI")
	s.env["typed"] = token
	s.ok("login", home.URL, "--profile", "home")
	want(t, s.ok("login", lab.URL, "--profile", "lab"), "pail profiles use lab")

	// The default wins, and on a terminal pail says where it's pointing.
	_, _, stderr := s.run("ls")
	want(t, stderr, "→ home · "+home.URL)
	_, _, stderr = s.run("ls", "-p", "lab")
	want(t, stderr, "→ lab · "+lab.URL)
	s.env["PAIL_PROFILE"] = "lab"
	_, _, stderr = s.run("ls")
	want(t, stderr, "→ lab · "+lab.URL)
	delete(s.env, "PAIL_PROFILE")
	want(t, s.fails(ExitUsage, "ls", "-p", "shed"), "No profile called shed in ~/.pail/config. You have: home, lab.")

	// PAIL_URL needs no file, and beats the default.
	s.env["PAIL_URL"], s.env["PAIL_TOKEN"] = lab.URL, token
	_, _, stderr = s.run("ls")
	want(t, stderr, "→ PAIL_URL · "+lab.URL)
	delete(s.env, "PAIL_URL")
	delete(s.env, "PAIL_TOKEN")

	// Without a default, two profiles are ambiguous; one is not.
	s.ok("profiles", "use", "lab")
	want(t, s.ok("profiles"), "* lab", "  home")
	s.ok("profiles", "rm", "lab")
	s.ok("ls")
	s.ok("login", lab.URL, "--profile", "lab")
	cfg := filepath.Join(s.home, ".pail", "config")
	b, _ := os.ReadFile(cfg)
	if strings.Contains(string(b), "default") {
		t.Fatalf("removing the default profile should leave no default:\n%s", b)
	}
	want(t, s.fails(ExitUsage, "ls"), "More than one Pail installation in ~/.pail/config. Pick one with --profile home or --profile lab.")

	if runtime.GOOS != "windows" {
		os.Chmod(cfg, 0o644)
		want(t, s.fails(ExitUsage, "ls", "-p", "home"), "can be read by other users", "chmod 600 ~/.pail/config")
	}

	// PAIL_CONFIG moves the file.
	s.env["PAIL_CONFIG"] = filepath.Join(s.home, "elsewhere", "config.toml")
	s.ok("login", home.URL)
	want(t, s.ok("profiles", "--json"), `"name": "127-0-0-1"`, `"default": true`)
}

func TestUsage(t *testing.T) {
	s := newShell(t)
	want(t, s.ok(), "pail login <url>")
	want(t, s.ok("--help"), "pail up [dir]")
	want(t, s.ok("--version"), "pail test")
	want(t, s.fails(ExitUsage, "frobnicate"), "No command called frobnicate.")
	want(t, s.fails(ExitUsage, "ls", "--nope"), "Unknown flag: --nope")
	want(t, s.fails(ExitUsage, "ls", "--name", "x"), "--name only goes with pail up.")
}

func TestDeploysAndRollback(t *testing.T) {
	ts := installation(t)
	s := newShell(t)
	s.env["PAIL_URL"], s.env["PAIL_TOKEN"] = ts.URL, token

	s.write("dist/index.html", "v1")
	s.ok("up", "./dist", "--name", "blog")
	s.write("dist/index.html", "v2")
	s.ok("up", "./dist", "--name", "blog")

	ids := strings.Fields(s.ok("deploys", "blog", "--quiet"))
	if len(ids) != 2 {
		t.Fatalf("deploys --quiet: %q", ids)
	}
	newest, oldest := ids[0], ids[1]
	table := strings.Split(s.ok("deploys", "blog"), "\n")
	want(t, table[0], "DEPLOY", "WHEN", "WHAT")
	want(t, table[1], newest, "just now", "pail up from 127.0.0.1", "Serving")
	if strings.Contains(table[2], "Serving") {
		t.Errorf("only one deploy is served:\n%s", strings.Join(table, "\n"))
	}

	want(t, s.ok("rollback", "blog", oldest), "blog is serving "+oldest+".", "http://blog.pail.lan")
	if got := fetch(t, ts, "blog.pail.lan", "/"); got != "v1" {
		t.Errorf("after rollback the site serves %q", got)
	}
	table = strings.Split(s.ok("deploys", "blog"), "\n")
	want(t, table[2], oldest, "Serving")
	want(t, s.ok("deploys", "blog", "--json"), `"serving": "`+oldest+`"`)
	want(t, s.ok("ls"), oldest) // the list shows what's being served

	want(t, s.fails(ExitNotFound, "rollback", "blog", "0000000"), "no deploy 0000000")
	want(t, s.fails(ExitNotFound, "deploys", "nope"), "No pail called nope.")
	want(t, s.fails(ExitUsage, "rollback", "blog"), "pail rollback takes a pail and a deploy")

	s.write("bad/pail.json", `{"functions": {"api": {"src": "./api"}}}`)
	s.fails(ExitDeployFailed, "up", "./bad", "--name", "blog")
	failed := strings.Fields(s.ok("deploys", "blog", "-q"))[0]
	want(t, s.ok("deploys", "blog"), "Failed")
	want(t, s.fails(ExitUsage, "rollback", "blog", failed), "didn't finish")
}

func TestRedeployStopStartOpenRm(t *testing.T) {
	ts := installation(t)
	s := newShell(t)
	s.env["PAIL_URL"], s.env["PAIL_TOKEN"] = ts.URL, token
	port := ts.URL[strings.LastIndex(ts.URL, ":"):]
	url := "http://blog.pail.lan" + port

	s.write("dist/index.html", "v1")
	s.ok("up", "./dist", "--name", "blog")
	s.write("dist/index.html", "v2")
	s.ok("up", "./dist", "--name", "blog")
	ids := strings.Fields(s.ok("deploys", "blog", "-q"))
	v2, v1 := ids[0], ids[1]

	// Redeploy makes a new deploy of the latest good files, even after a
	// rollback, and follows it like pail up does.
	s.ok("rollback", "blog", v1)
	code, stdout, stderr := s.run("redeploy", "blog")
	if code != ExitOK || stdout != url+"\n" {
		t.Fatalf("redeploy: exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	want(t, stderr, "redeploy of "+v2, "copying 1 file from "+v2, "✓ live at "+url)
	if got := fetch(t, ts, "blog.pail.lan", "/"); got != "v2" {
		t.Errorf("after redeploy the site serves %q", got)
	}
	if ids := strings.Fields(s.ok("deploys", "blog", "-q")); len(ids) != 3 {
		t.Errorf("redeploy should add a deploy: %v", ids)
	}
	want(t, s.fails(ExitNotFound, "redeploy", "nope"), "No pail called nope.")

	// Stop keeps everything but answers nothing; start brings it back.
	want(t, s.ok("stop", "blog"), "blog is off.", "pail start blog")
	want(t, s.ok("ls"), "Off")
	want(t, fetch(t, ts, "blog.pail.lan", "/"), "blog is off. Start it with pail start blog.")
	want(t, s.ok("start", "blog"), "blog is back on.", url)
	want(t, s.ok("ls"), "Live")
	if got := fetch(t, ts, "blog.pail.lan", "/"); got != "v2" {
		t.Errorf("after start the site serves %q", got)
	}
	want(t, s.fails(ExitNotFound, "stop", "nope"), "No pail called nope.")

	if got := s.ok("open", "blog"); got != url+"\n" || len(s.opened) != 1 || s.opened[0] != url {
		t.Errorf("open: stdout %q, opened %v", got, s.opened)
	}

	// rm asks first. With no terminal it needs --yes.
	want(t, s.fails(ExitUsage, "rm", "blog"), "Add --yes.")
	s.tty = true
	s.typed = "n\n"
	_, stdout, stderr = s.run("rm", "blog")
	want(t, stderr, "Remove blog? "+url+" stops answering, and every deploy is deleted. This can't be undone.")
	want(t, stdout, "Kept blog.")
	want(t, s.ok("ls"), "blog")
	s.typed = "y\n"
	want(t, s.ok("rm", "blog"), "Removed blog.")
	want(t, s.ok("ls"), "Nothing in the pail yet.")
	s.tty = false

	s.ok("up", "./dist", "--name", "blog")
	want(t, s.ok("rm", "blog", "--yes"), "Removed blog.")
	want(t, s.fails(ExitNotFound, "rm", "blog", "--yes"), "No pail called blog.")
}

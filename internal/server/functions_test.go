package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisdmacrae/pail/internal/microvm"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/storage"
)

// The fake's functions: a "code disk" is the source files as JSON, and a
// copy "runs" a program by reading it as a script of its own. A line is
// output, with $VARIABLES filled in from the request, unless it starts with
// an exclamation mark:
//
//	!stderr text   writes text to standard error
//	!sleep 100ms   takes that long
//	!exit 3        ends with that status
//	!timeout       overruns, and is stopped
//	!oom           runs out of memory
//	!crash         takes its microVM down with it

func (f *fakeMachines) BuildFunction(_ context.Context, req microvm.FunctionBuild) (microvm.FunctionImage, error) {
	var img microvm.FunctionImage
	src, err := os.MkdirTemp("", "pail-fake-fn-")
	if err != nil {
		return img, err
	}
	defer os.RemoveAll(src)
	if err := req.Fill(src); err != nil {
		return img, err
	}
	files := map[string]string{}
	filepath.WalkDir(src, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			rel, _ := filepath.Rel(src, p)
			b, _ := os.ReadFile(p)
			files[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	if req.Script != "" {
		req.Log("built in " + req.BuildImage)
		for _, line := range strings.Split(req.Script, "\n") {
			if strings.HasPrefix(line, "echo \"→") {
				req.Log(strings.Trim(strings.TrimPrefix(line, "echo "), `"`))
			}
		}
	}
	for _, body := range files {
		if strings.Contains(body, "BROKEN") {
			return img, errors.New("the build exited with status 1")
		}
	}
	os.MkdirAll(req.Dir, 0o755)
	code, _ := json.Marshal(files)
	img = microvm.FunctionImage{
		Root: filepath.Join(req.Dir, "root.ext4"), Code: filepath.Join(req.Dir, "code.ext4"),
		RootID: "fake-" + req.RunImage, Env: []string{"PATH=/bin"},
	}
	os.WriteFile(img.Root, []byte(req.RunImage), 0o644)
	os.WriteFile(img.Code, code, 0o644)
	if !f.noSnapshots {
		img.Mem, img.State = filepath.Join(req.Dir, "mem"), filepath.Join(req.Dir, "state")
		os.WriteFile(img.Mem, []byte("memory"), 0o644)
		os.WriteFile(img.State, []byte("state"), 0o644)
	}
	return img, nil
}

type fakeCopy struct {
	owner *fakeMachines
	files map[string]string
	root  string
	// tmp is what calls to this copy have left behind.
	calls int
	done  chan struct{}
	once  sync.Once
}

func (f *fakeMachines) StartFunction(_ context.Context, spec microvm.FunctionSpec) (microvm.FunctionCopy, error) {
	code, err := os.ReadFile(spec.Image.Code)
	if err != nil {
		return nil, err
	}
	root, err := os.ReadFile(spec.Image.Root)
	if err != nil {
		return nil, err
	}
	c := &fakeCopy{owner: f, root: string(root), done: make(chan struct{})}
	if err := json.Unmarshal(code, &c.files); err != nil {
		return nil, err
	}
	if spec.Image.State != "" {
		if _, err := os.Stat(spec.Image.State); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.copies++
	f.most = max(f.most, f.copies)
	f.mu.Unlock()
	return c, nil
}

func (c *fakeCopy) Done() <-chan struct{} { return c.done }
func (c *fakeCopy) Stop() {
	c.once.Do(func() {
		c.owner.mu.Lock()
		c.owner.copies--
		c.owner.mu.Unlock()
		close(c.done)
	})
}

func (c *fakeCopy) Call(_ context.Context, call microvm.FunctionCall) (microvm.FunctionResult, error) {
	var res microvm.FunctionResult
	// The program is the file the command names, or the command itself.
	program, ok := c.files[strings.TrimPrefix(call.Argv[len(call.Argv)-1], "./")]
	if !ok {
		program = call.Argv[len(call.Argv)-1]
	}
	body, _ := io.ReadAll(io.LimitReader(call.Body, call.BodyLen))
	env := map[string]string{"BODY": string(body), "ARGV": strings.Join(call.Argv, " "), "ROOT": c.root, "CALLS": strconv.Itoa(c.calls), "DIR": call.Dir}
	for _, kv := range call.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	c.calls++
	var out []string
	for _, line := range strings.Split(program, "\n") {
		word, rest, _ := strings.Cut(line, " ")
		switch word {
		case "!stderr":
			call.Stderr(rest)
		case "!sleep":
			d, _ := time.ParseDuration(rest)
			time.Sleep(d)
		case "!exit":
			res.ExitCode, _ = strconv.Atoi(rest)
		case "!timeout":
			res.TimedOut = true
		case "!oom":
			res.OutOfMemory, res.Signal, res.ExitCode = true, "killed", 137
		case "!crash":
			c.Stop()
			return res, errors.New("the function's microVM stopped answering")
		default:
			out = append(out, os.Expand(line, func(k string) string { return env[k] }))
		}
	}
	res.Output = []byte(strings.Join(out, "\n"))
	return res, nil
}

func (f *fakeMachines) awake() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copies
}

func TestFunctions(t *testing.T) {
	store := storage.NewMemory()
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, store, machines, t.TempDir())
	project := func(version string) map[string]string {
		return map[string]string{
			"public/index.html": "the page of " + version,
			"fn/hello/main.sh": "Content-Type: application/json\nX-Version: " + version + "\n\n" +
				`{"version": "` + version + `", "method": "$REQUEST_METHOD", "path": "$PATH_INFO", "query": "$QUERY_STRING", "body": "$BODY", "type": "$CONTENT_TYPE", "length": "$CONTENT_LENGTH", "agent": "$HTTP_USER_AGENT", "greeting": "$GREETING", "pail": "$PAIL_NAME", "deploy": "$PAIL_DEPLOY", "home": "$HOME", "argv": "$ARGV", "root": "$ROOT", "dir": "$DIR", "calls": $CALLS}`,
			"fn/odd.py":              "Status: 404 Not Found\n\nNo such recipe.",
			"fn/app/package.json":    `{"main": "server.js"}`,
			"fn/app/server.js":       "x",
			"fn/redir.rb":            "Location: /elsewhere\n\n",
			"fn/fail/main.sh":        "!stderr about to fail\n!exit 3\nContent-Type: text/plain\n\nnever seen",
			"fn/slow/main.sh":        "!timeout",
			"fn/hungry/main.sh":      "!oom",
			"fn/silent/main.sh":      "just words, no headers",
			"fn/crashy/main.sh":      "!crash",
			"fn/plain/main.sh":       "\nno headers at all, only a body",
			"fn/custom/whatever.txt": "x",
			"pail.json": `{
				"static": "./public",
				"functions": {
					"hello": {"src": "./fn/hello", "env": {"GREETING": "hi", "REQUEST_METHOD": "SPOOFED"}},
					"odd": {"src": "./fn/odd.py", "memory": "256MB"},
					"app": {"src": "./fn/app"}, "redir": {"src": "./fn/redir.rb"},
					"fail": {"src": "fn/fail"}, "slow": {"src": "fn/slow", "timeout": "2s"}, "hungry": {"src": "fn/hungry"},
					"silent": {"src": "fn/silent"}, "crashy": {"src": "fn/crashy"}, "plain": {"src": "fn/plain"},
					"custom": {"src": "fn/custom", "lang": "shell", "cmd": "Content-Type: text/x-custom\n\nfrom the command line"}
				},
				"routes": [
					{"path": "/api/*", "to": "function:hello"}, {"path": "/odd", "to": "function:odd"}, {"path": "/app/*", "to": "function:app"}, {"path": "/go", "to": "function:redir"},
					{"path": "/fail", "to": "function:fail"}, {"path": "/slow", "to": "function:slow"}, {"path": "/hungry", "to": "function:hungry"},
					{"path": "/silent", "to": "function:silent"}, {"path": "/crashy", "to": "function:crashy"}, {"path": "/plain", "to": "function:plain"},
					{"path": "/custom", "to": "function:custom"},
					{"path": "/*", "to": "static"}
				]
			}`,
		}
	}
	output := func() string {
		return f.api("GET", "/api/v1/pails/dash/output?follow=false", nil).Body.String()
	}

	v1, log := f.deploy("dash", tarGz(t, project("v1")))
	if v1.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", v1, log)
	}
	for _, want := range []string{"preparing hello (shell)", "hello is ready · snapshotted", "preparing odd (python)", "building app (node) in a microVM", "built in node:22-slim", "→ npm install", "swapping dash.pail.lan"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// Nothing runs until something is asked for.
	if n := machines.awake(); n != 0 {
		t.Errorf("%d copies running before any request", n)
	}

	// A request arrives as CGI, and the answer's headers are the response's.
	rec := f.do("POST", "dash.pail.lan", "/api/notes/7?x=1&y=2", []byte("a note"), "Content-Type", "text/markdown", "User-Agent", "tests")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Version") != "v1" {
		t.Fatalf("the answer: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the answer isn't what the function wrote: %v\n%s", err, rec.Body)
	}
	for key, want := range map[string]any{
		"method": "POST", "path": "/api/notes/7", "query": "x=1&y=2", "body": "a note", "type": "text/markdown", "length": "6",
		"agent": "tests", "greeting": "hi", "pail": "dash", "deploy": v1.ID, "home": "/tmp", "argv": "sh main.sh", "root": "alpine:3", "dir": "/work/src", "calls": float64(0),
	} {
		if got[key] != want {
			t.Errorf("%s: got %v, want %v", key, got[key], want)
		}
	}
	// The copy stays warm for the next request.
	json.Unmarshal(f.site("dash.pail.lan", "/api/again").Body.Bytes(), &got)
	if got["calls"] != float64(1) || machines.awake() != 1 {
		t.Errorf("the second request: call %v on %d copies", got["calls"], machines.awake())
	}
	if status := f.api("GET", "/api/v1/pails/dash", nil).Body.String(); !strings.Contains(status, `{"name":"hello","lang":"shell","copies":1,"max":4}`) {
		t.Errorf("status: %s", status)
	}

	// What a program can say about its answer.
	wantBody(t, f.site("dash.pail.lan", "/odd"), 404, "No such recipe.")
	if rec := f.site("dash.pail.lan", "/odd"); rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("with no Content-Type: %v", rec.Header())
	}
	if rec := f.site("dash.pail.lan", "/go"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/elsewhere" {
		t.Errorf("a redirect: %d %v", rec.Code, rec.Header())
	}
	wantBody(t, f.site("dash.pail.lan", "/plain"), 200, "no headers at all, only a body")
	if rec := f.site("dash.pail.lan", "/custom"); rec.Body.String() != "from the command line" || rec.Header().Get("Content-Type") != "text/x-custom" {
		t.Errorf("a function with its own cmd: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := f.do("HEAD", "dash.pail.lan", "/odd", nil); rec.Code != 404 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", rec.Code, rec.Body)
	}
	// Files and the source: one is served, the other never.
	wantBody(t, f.site("dash.pail.lan", "/"), 200, "the page of v1")
	wantBody(t, f.site("dash.pail.lan", "/fn/hello/main.sh"), 404, "Nothing at")

	// How a request can go wrong. Each is a 500 that says why, there and in
	// the pail's output.
	for path, want := range map[string]string{
		"/fail":   "fail exited with status 3.",
		"/slow":   "slow ran longer than its timeout of 2s and was stopped.",
		"/hungry": "hungry ran out of memory: it has 128MB.",
		"/silent": "silent in dash answered in a way Pail couldn't read.",
		"/crashy": "crashy couldn't run.",
	} {
		rec := f.site("dash.pail.lan", path)
		if rec.Code != 500 || !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), "pail logs dash --output") {
			t.Errorf("%s: got %d %q, want a 500 saying %q", path, rec.Code, rec.Body, want)
		}
	}
	for _, want := range []string{`"text":"about to fail","source":"fail"`, "fail exited with status 3.", "slow ran longer than", "hungry ran out of memory", "silent answered, but not as CGI", "crashy's microVM failed"} {
		if !strings.Contains(output(), want) {
			t.Errorf("the pail's output lacks %q:\n%s", want, output())
		}
	}
	// A copy that overran, or whose microVM died, isn't used again.
	if status := f.api("GET", "/api/v1/pails/dash", nil).Body.String(); !strings.Contains(status, `{"name":"slow","lang":"shell","copies":0,"max":4}`) || !strings.Contains(status, `{"name":"fail","lang":"shell","copies":1,"max":4}`) {
		t.Errorf("status after the failures: %s", status)
	}

	// A new deploy answers from then on, and the old one's copies stop.
	v2, _ := f.deploy("dash", tarGz(t, project("v2")))
	if v2.State != "ok" {
		t.Fatalf("second deploy: %+v", v2)
	}
	if n := machines.awake(); n != 0 {
		t.Errorf("%d copies of the old deploy still running", n)
	}
	wantBody(t, f.site("dash.pail.lan", "/api/x"), 200, `"version": "v2"`)
	// Rolling back restores what that deploy snapshotted.
	wantBody(t, f.api("POST", "/api/v1/pails/dash/serve", []byte(`{"deploy":"`+v1.ID+`"}`)), 200, `"serving":"`+v1.ID+`"`)
	wantBody(t, f.site("dash.pail.lan", "/api/x"), 200, `"version": "v1"`)

	// A stopped pail runs nothing; started, its functions wake on request.
	wantBody(t, f.api("POST", "/api/v1/pails/dash/stop", nil), 200, `"status":"off"`)
	if n := machines.awake(); n != 0 {
		t.Errorf("%d copies running in a stopped pail", n)
	}
	wantBody(t, f.site("dash.pail.lan", "/api/x"), http.StatusServiceUnavailable, "dash is off")
	wantBody(t, f.api("POST", "/api/v1/pails/dash/start", nil), 200, `"status":"live"`)
	eventually(t, "the function answers after a start", func() bool { return f.site("dash.pail.lan", "/api/x").Code == 200 })

	// Pail restarting loses nothing: the function's files come back from
	// storage if this machine no longer has them.
	f.svc.Close()
	os.RemoveAll(filepath.Join(f.dir, "functions"))
	f = newContainerFixture(t, store, machines, f.dir)
	eventually(t, "the function answers after a restart", func() bool {
		rec := f.site("dash.pail.lan", "/api/x")
		return rec.Code == 200 && strings.Contains(rec.Body.String(), `"version": "v1"`)
	})

	// Redeploy runs the same build again as a new deploy.
	rec = f.api("POST", "/api/v1/pails/dash/redeploy", nil)
	wantBody(t, rec, http.StatusAccepted, "redeploy of")
	f.svc.Wait()
	var again deployResult
	json.Unmarshal(rec.Body.Bytes(), &again)
	wantBody(t, f.site("dash.pail.lan", "/api/x"), 200, `"deploy": "`+again.ID+`"`)

	// Removing the pail stops its copies and deletes what it kept.
	wantBody(t, f.api("DELETE", "/api/v1/pails/dash", nil), http.StatusNoContent, "")
	if n := machines.awake(); n != 0 {
		t.Errorf("%d copies running after the pail was removed", n)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "functions/dash")); !os.IsNotExist(err) {
		t.Errorf("the function's files outlived its pail")
	}
	if keys, _ := store.List(context.Background(), "meta/dash/"); len(keys) != 0 {
		t.Errorf("left in storage: %v", keys)
	}
}

// Copies: started as requests need them, up to max, and stopped when idle.
func TestFunctionCopies(t *testing.T) {
	machines := &fakeMachines{t: t, volumes: map[string]bool{}, noSnapshots: true}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir())
	deploy := func(settings string) {
		t.Helper()
		d, log := f.deploy("busy", tarGz(t, map[string]string{
			"main.sh":   "!sleep 150ms\nContent-Type: text/plain\n\ndone",
			"pail.json": `{"functions": {"work": {"src": "."` + settings + `}}}`,
		}))
		if d.State != "ok" || strings.Contains(log, "snapshotted") {
			t.Fatalf("deploy: %+v\n%s", d, log)
		}
	}
	// burst sends n requests at once and returns their statuses.
	burst := func(n int) map[int]int {
		codes := map[int]int{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := f.site("busy.pail.lan", "/")
				mu.Lock()
				codes[rec.Code]++
				mu.Unlock()
			}()
		}
		wg.Wait()
		return codes
	}

	// One function and no routes: it answers every path. Three requests at
	// once, with room for two copies: the third waits its turn.
	deploy(`, "max": 2, "idle": "100ms"`)
	wantBody(t, f.site("busy.pail.lan", "/any/path"), 200, "done")
	if codes := burst(3); codes[200] != 3 || machines.most != 2 {
		t.Errorf("three at once with max 2: %v, on %d copies at most", codes, machines.most)
	}
	// Idle, they stop: a burst scales up and back down to nothing.
	eventually(t, "idle copies stopped", func() bool { return machines.awake() == 0 })
	wantBody(t, f.site("busy.pail.lan", "/"), 200, "done")

	// A request that waits longer than its timeout for a copy is turned
	// away, and the pail's output says the function was at its max.
	deploy(`, "max": 1, "timeout": "60ms"`)
	if codes := burst(2); codes[200] != 1 || codes[503] != 1 {
		t.Errorf("two at once with max 1 and a short timeout: %v", codes)
	}
	wantBody(t, f.api("GET", "/api/v1/pails/busy/output?follow=false", nil), 200, "work was at its max of 1 copy, and a request waited 60ms for one.")
}

func TestFunctionPailJSON(t *testing.T) {
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir())
	with := func(pailJSON string, extra ...string) []byte {
		files := map[string]string{"pail.json": pailJSON, "fn/main.sh": "x", "fn/notes.txt": "x", "docs/readme.md": "x", "go/go.mod": "module x", "node/package.json": "{}"}
		for i := 0; i+1 < len(extra); i += 2 {
			files[extra[i]] = extra[i+1]
		}
		return tarGz(t, files)
	}
	for want, pailJSON := range map[string]string{
		`say where api's source is, like "src": "./fn/api"`:                                                                       `{"functions": {"api": {}}}`,
		"api's src is ./nowhere, and the upload has nothing there":                                                                `{"functions": {"api": {"src": "./nowhere"}}}`,
		"Pail can't tell what language api is in: ./docs has readme.md.":                                                          `{"functions": {"api": {"src": "./docs"}}}`,
		`api's lang is "cobol", and Pail runs python, node, ruby, go, rust, shell`:                                                `{"functions": {"api": {"src": "./fn", "lang": "cobol"}}}`,
		"api is python, which runs main.py, and ./fn has no such file":                                                            `{"functions": {"api": {"src": "./fn", "lang": "python"}}}`,
		"api asks for 2GB of memory, and this Pail gives a function at most 1GB. Ask for less, or raise PAIL_MAX_FUNCTION_MEMORY": `{"functions": {"api": {"src": "./fn", "memory": "2GB"}}}`,
		"api's timeout must be a length of time up to 15m0s, like 10s":                                                            `{"functions": {"api": {"src": "./fn", "timeout": "soon"}}}`,
		"api's idle must be a length of time, like 5m":                                                                            `{"functions": {"api": {"src": "./fn", "idle": "-1s"}}}`,
		"api's max must be between 1 and 64. Got 0":                                                                               `{"functions": {"api": {"src": "./fn", "max": 0}}}`,
		"function names are lowercase letters":                                                                                    `{"functions": {"My API": {"src": "./fn"}}}`,
		"declares no function by that name":                                                                                       `{"functions": {"api": {"src": "./fn"}}, "routes": [{"path": "/*", "to": "function:other"}]}`,
		"needs routes to say which paths go where":                                                                                `{"functions": {"api": {"src": "./fn"}, "two": {"src": "./fn"}}}`,
		`A route goes to "static", "function:<name>" or "container:<name>"`:                                                       `{"functions": {"api": {"src": "./fn"}}, "routes": [{"path": "/*", "to": "lambda:api"}]}`,
		"api didn't build: the build exited with status 1":                                                                        `{"functions": {"api": {"src": "./broken"}}}`,
	} {
		if d, _ := f.deploy("wrong", with(pailJSON, "broken/go.mod", "BROKEN")); d.State != "failed" || !strings.Contains(d.Error, want) {
			t.Errorf("%s: got %+v, want an error containing %q", pailJSON, d, want)
		}
	}

	// Languages are told apart by what the source holds, and built if they
	// need it.
	d, log := f.deploy("langs", with(`{"functions": {"sh": {"src": "./fn"}, "go": {"src": "./go", "timeout": 30, "memory": 64}, "node": {"src": "./node"}, "one": {"src": "./x/thing.rb"}},
		"routes": [{"path": "/sh", "to": "function:sh"}, {"path": "/go", "to": "function:go"}, {"path": "/node", "to": "function:node"}, {"path": "/one", "to": "function:one"}]}`,
		"go/fn", "Content-Type: text/plain\n\n$ARGV on $ROOT", "node/.pail-main", "x", "x/thing.rb", "Content-Type: text/plain\n\n$ARGV on $ROOT"))
	if d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	for _, want := range []string{"preparing sh (shell)", "building go (go) in a microVM", "built in golang:1.25-alpine", "→ go build -o fn", "building node (node)", "preparing one (ruby)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	wantBody(t, f.site("langs.pail.lan", "/go"), 200, "./fn on alpine:3")
	wantBody(t, f.site("langs.pail.lan", "/one"), 200, "ruby .pail-handler.rb thing.rb on ruby:3.4-slim")

	// Where microVMs can't run, a deploy with functions is refused, saying why.
	machines.down = "this machine has no /dev/kvm"
	if d, _ := f.deploy("nowhere", with(`{"functions": {"api": {"src": "./fn"}}}`)); d.State != "failed" || !strings.Contains(d.Error, "this Pail can't run it: this machine has no /dev/kvm") {
		t.Errorf("with no microVMs: %+v", d)
	}
	_ = httptest.NewRecorder
	_ = pails.ErrFunctionBusy
}

// A build can leave a pail.json of its own, as a framework's adapter does:
// what it made is then files and a function, not one folder of files.
func TestBuildsThatLeaveAPailJSON(t *testing.T) {
	store := storage.NewMemory()
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, store, machines, t.TempDir())
	project := map[string]string{
		"package.json":          `{"scripts": {"build": "astro build"}}`,
		"src/pages/index.astro": "the page's source",
		"makes/pail.json": `{
			"static": "./client",
			"functions": {"ssr": {"src": "./server", "lang": "node", "cmd": ["node", "entry.mjs"], "memory": "256MB"}},
			"routes": [{"path": "/", "to": "static"}, {"path": "/_astro/*", "to": "static"}, {"path": "/*", "to": "function:ssr"}]
		}`,
		"makes/client/index.html":     "the prerendered page",
		"makes/client/_astro/app.css": "h1{}",
		"makes/server/entry.mjs":      "Content-Type: text/html\n\nrendered $PATH_INFO on demand with $ARGV",
		"makes/server/package.json":   `{"dependencies": {"sharp": "0.35.5"}}`,
	}

	d, log := f.deploy("garden", tarGz(t, project))
	if d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	for _, want := range []string{"found package.json with a build script", "→ npm run build", "built ./dist · 7 files · found pail.json", "building ssr (node) in a microVM", "→ npm install", "ssr is ready"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	wantBody(t, f.site("garden.pail.lan", "/"), 200, "the prerendered page")
	wantBody(t, f.site("garden.pail.lan", "/_astro/app.css"), 200, "h1{}")
	wantBody(t, f.site("garden.pail.lan", "/blog/7"), 200, "rendered /blog/7 on demand with node entry.mjs")
	// Neither the function's files nor the project's source are served.
	for _, path := range []string{"/server/entry.mjs", "/pail.json", "/src/pages/index.astro"} {
		wantBody(t, f.site("garden.pail.lan", path), 200, "rendered "+path+" on demand")
	}

	// A redeploy runs what was built then, with nothing built again.
	wantBody(t, f.api("POST", "/api/v1/pails/garden/redeploy", nil), http.StatusAccepted, "redeploy of")
	eventually(t, "the redeploy goes live", func() bool {
		return !strings.Contains(f.api("GET", "/api/v1/pails/garden", nil).Body.String(), `"serving":"`+d.ID+`"`)
	})
	wantBody(t, f.site("garden.pail.lan", "/"), 200, "the prerendered page")
	wantBody(t, f.site("garden.pail.lan", "/blog/8"), 200, "rendered /blog/8 on demand")

	// What the build's pail.json gets wrong fails the deploy, and says so.
	project["makes/pail.json"] = `{"static": "./client", "functions": {"ssr": {"src": "./nowhere", "lang": "node"}}, "routes": [{"path": "/*", "to": "function:ssr"}]}`
	bad, log := f.deploy("garden", tarGz(t, project))
	if bad.State != "failed" || !strings.Contains(bad.Error, "ssr's src is ./nowhere") {
		t.Errorf("a build with a wrong pail.json: %+v\n%s", bad, log)
	}
	wantBody(t, f.site("garden.pail.lan", "/blog/9"), 200, "rendered /blog/9 on demand")
}

// A Go function needs nothing of Pail's to be a handler: the standard
// library's net/http/cgi reads what Pail sends and writes what Pail reads.
func TestGoFunctionsServeCGI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module api\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
)

func handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Set-Cookie", "a=1")
	w.Header().Add("Set-Cookie", "b=2")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"method": r.Method, "path": r.URL.Path, "page": r.URL.Query().Get("page"),
		"agent": r.UserAgent(), "host": r.Host, "body": string(body),
	})
}

func main() {
	cgi.Serve(http.HandlerFunc(handler))
}
`), 0o644)
	bin := filepath.Join(dir, "fn")
	if out, err := exec.Command("go", "build", "-C", dir, "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	req := httptest.NewRequest("POST", "http://dash.pail.lan/api/notes/7?page=2", strings.NewReader("a note"))
	req.Header.Set("User-Agent", "tests")
	cmd := exec.Command(bin)
	cmd.Env, cmd.Stdin = cgiEnv(req, 6), req.Body
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the function: %v", err)
	}
	status, header, body, err := parseCGI(out)
	if err != nil || status != http.StatusCreated || header.Get("Content-Type") != "application/json" || len(header.Values("Set-Cookie")) != 2 {
		t.Fatalf("the answer: %d %v, %v\n%s", status, header, err, out)
	}
	var got map[string]string
	json.Unmarshal(body, &got)
	for key, want := range map[string]string{"method": "POST", "path": "/api/notes/7", "page": "2", "agent": "tests", "host": "dash.pail.lan", "body": "a note"} {
		if got[key] != want {
			t.Errorf("%s: got %q, want %q", key, got[key], want)
		}
	}
}

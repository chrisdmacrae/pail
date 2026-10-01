package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/microvm"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/storage"
)

// fakeMachines stands in for Firecracker. A "root filesystem" is a file
// holding the Dockerfile it was built from, and a "machine" is a local HTTP
// server that says which one it booted.
type fakeMachines struct {
	fakeBuilder
	t *testing.T

	mu      sync.Mutex
	running []*fakeMachine
	// volumes are the data volumes in use, to catch two machines sharing one.
	volumes map[string]bool
	// digests is the registry: what each image's tag points at now.
	digests map[string]string
	// pulls counts the images fetched in full.
	pulls int
	// copies are the function copies running, and most the most there
	// have been at once.
	copies, most int
	// noSnapshots makes function builds leave no snapshot.
	noSnapshots bool
}

type fakeMachine struct {
	owner  *fakeMachines
	spec   microvm.MachineSpec
	srv    *httptest.Server
	addr   string
	done   chan struct{}
	once   sync.Once
	killed bool // stopped by Stop, rather than by crashing
}

func (f *fakeMachines) BuildContainer(_ context.Context, req microvm.ContainerBuild) (microvm.Built, error) {
	dir, err := os.MkdirTemp("", "pail-fake-image-")
	if err != nil {
		return microvm.Built{}, err
	}
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	if err := req.Fill(src); err != nil {
		return microvm.Built{}, err
	}
	dockerfile, err := os.ReadFile(filepath.Join(src, req.Dockerfile))
	if err != nil {
		return microvm.Built{}, err
	}
	req.Log("STEP 1/1: " + strings.SplitN(string(dockerfile), "\n", 2)[0])
	if strings.Contains(string(dockerfile), "BROKEN") {
		return microvm.Built{}, errors.New("the build exited with status 1")
	}
	img := &microvm.Image{Path: filepath.Join(dir, "root.ext4"), Env: []string{"FROM_IMAGE=yes"}, Cmd: []string{"serve"}, WorkingDir: "/srv"}
	if strings.Contains(string(dockerfile), "NOCMD") {
		img.Cmd = nil
	}
	os.WriteFile(img.Path, dockerfile, 0o644)
	return microvm.Built{Image: img, Cleanup: func() { os.RemoveAll(dir) }}, nil
}

func (f *fakeMachines) PullContainer(_ context.Context, req microvm.ContainerPull) (microvm.Built, error) {
	f.mu.Lock()
	digest := f.digests[req.Ref]
	f.mu.Unlock()
	switch {
	case digest == "":
		return microvm.Built{}, errors.New("can't pull " + req.Ref + ": no such image")
	case digest == req.Unless:
		return microvm.Built{Digest: digest}, nil
	}
	f.mu.Lock()
	f.pulls++
	f.mu.Unlock()
	dir, err := os.MkdirTemp("", "pail-fake-image-")
	if err != nil {
		return microvm.Built{}, err
	}
	req.Log("pulling " + req.Ref)
	img := &microvm.Image{Path: filepath.Join(dir, "root.ext4"), Entrypoint: []string{"/entry"}, Cmd: []string{"run"}}
	if strings.Contains(digest, "nocmd") {
		img.Entrypoint, img.Cmd = nil, nil
	}
	os.WriteFile(img.Path, []byte("IMAGE "+req.Ref+"@"+digest), 0o644)
	return microvm.Built{Image: img, Digest: digest, Cleanup: func() { os.RemoveAll(dir) }}, nil
}

func (f *fakeMachines) Start(_ context.Context, spec microvm.MachineSpec) (microvm.Machine, error) {
	image, err := os.ReadFile(spec.Rootfs)
	if err != nil {
		return nil, err
	}
	m := &fakeMachine{owner: f, spec: spec, done: make(chan struct{})}
	boots := 0
	f.mu.Lock()
	if spec.Data != "" {
		if f.volumes[spec.Data] {
			f.t.Errorf("two machines were given the volume %s at once", spec.Data)
		}
		f.volumes[spec.Data] = true
		raw, _ := os.ReadFile(spec.Data)
		boots, _ = strconv.Atoi(string(raw))
		boots++
		os.MkdirAll(filepath.Dir(spec.Data), 0o755)
		os.WriteFile(spec.Data, []byte(strconv.Itoa(boots)), 0o644)
	}
	f.running = append(f.running, m)
	f.mu.Unlock()

	env := strings.Join(spec.Env, " ")
	spec.Log("booted " + strings.SplitN(string(image), "\n", 2)[0])
	if strings.Contains(string(image), "DEAF") {
		// A machine that runs and never opens its port.
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		m.addr = l.Addr().String()
		l.Close()
		return m, nil
	}
	m.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s %s|host=%s|proto=%s|boots=%d|body=%s|env=%s|argv=%v|dir=%s",
			strings.SplitN(string(image), "\n", 2)[0], r.Method, r.URL.RequestURI(), r.Host, r.Header.Get("X-Forwarded-Proto"), boots, body, env, spec.Argv, spec.WorkingDir)
	}))
	m.addr = m.srv.Listener.Addr().String()
	if _, needs, ok := strings.Cut(string(image), "NEEDS "); ok {
		// A machine that opens its port only once another in its group is
		// up, as an app waits for its database.
		m.srv.Listener.Close()
		go func() {
			for !f.up(spec.Group, strings.Fields(needs)[0]) {
				select {
				case <-m.done:
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
			l, err := net.Listen("tcp", m.addr)
			if err != nil {
				return
			}
			m.srv.Listener = l
			m.srv.Start()
		}()
		return m, nil
	}
	m.srv.Start()
	return m, nil
}

// up says whether a machine in a group is running under a name.
func (f *fakeMachines) up(group, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.running {
		if group != "" && m.spec.Group == group && m.spec.Hostname == name {
			return true
		}
	}
	return false
}

func (m *fakeMachine) Addr(int) string       { return m.addr }
func (m *fakeMachine) Done() <-chan struct{} { return m.done }
func (m *fakeMachine) Stop()                 { m.end(true) }

func (m *fakeMachine) end(killed bool) {
	m.once.Do(func() {
		if m.srv != nil {
			m.srv.CloseClientConnections()
			m.srv.Close()
		}
		m.owner.mu.Lock()
		m.killed = killed
		delete(m.owner.volumes, m.spec.Data)
		for i, other := range m.owner.running {
			if other == m {
				m.owner.running = append(m.owner.running[:i], m.owner.running[i+1:]...)
				break
			}
		}
		m.owner.mu.Unlock()
		close(m.done)
	})
}

func (f *fakeMachines) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.running)
}

type containerFixture struct {
	*fixture
	machines *fakeMachines
	dir      string
}

func newContainerFixture(t *testing.T, store *storage.Memory, machines *fakeMachines, dir string, allowLAN ...string) *containerFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{Token: token, BaseDomain: "pail.lan", MaxUploadSize: 1 << 20, MaxDeploys: 3}
	svc := pails.New(pails.Options{Store: store, BaseDomain: cfg.BaseDomain, MaxDeploys: 3, Builder: machines, Dir: dir, MaxContainerMemory: 2 << 30, MaxFunctionMemory: 1 << 30, AllowLAN: allowLAN, SecretKey: []byte("0123456789abcdef0123456789abcdef"), Logger: logger})
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.Resume()
	t.Cleanup(svc.Close)
	f := &fixture{t: t, store: store, svc: svc, srv: New(Options{Config: cfg, Pails: svc, Logger: logger, Version: "test"})}
	return &containerFixture{fixture: f, machines: machines, dir: dir}
}

// eventually waits for something that happens off to the side.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// app is a small project with a page and a container behind /api.
func app(version, extra string) map[string]string {
	return map[string]string{
		"Dockerfile":        "FROM app:" + version + "\n" + extra,
		"server.js":         "the app's source",
		"public/index.html": "the page of " + version,
		"pail.json": `{
			"static": "./public",
			"containers": {"api": {"port": 8080, "data": "/data", "memory": "512MB", "env": {"GREETING": "hi"}}},
			"routes": [{"path": "/api/*", "to": "container:api"}, {"path": "/*", "to": "static"}]
		}`,
	}
}

func TestContainers(t *testing.T) {
	pails.PortWait, pails.RestartDelay = 300*time.Millisecond, 10*time.Millisecond
	store := storage.NewMemory()
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, store, machines, t.TempDir())
	get := func(path string) string { return f.site("notes.pail.lan", path).Body.String() }
	status := func() string { return f.api("GET", "/api/v1/pails/notes", nil).Body.String() }

	// A deploy builds the Dockerfile, boots it, and waits for its port.
	v1, log := f.deploy("notes", tarGz(t, app("v1", "")))
	if v1.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", v1, log)
	}
	for _, want := range []string{"building api from ./Dockerfile in a microVM", "STEP 1/1: FROM app:v1", "starting api in a microVM (1 vCPU, 512MB)", "api: booted FROM app:v1", "api is answering on port 8080", "swapping notes.pail.lan"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	// Routes: the container answers /api, with any method; files the rest.
	got := f.do("POST", "notes.pail.lan", "/api/notes?x=1", []byte("a note")).Body.String()
	for _, want := range []string{"FROM app:v1|POST /api/notes?x=1|host=notes.pail.lan|proto=http|boots=1|body=a note|", "FROM_IMAGE=yes", "PORT=8080", "PAIL_NAME=notes", "PAIL_DEPLOY=" + v1.ID, "GREETING=hi", "argv=[serve]|dir=/srv"} {
		if !strings.Contains(got, want) {
			t.Errorf("what the container saw lacks %q:\n%s", want, got)
		}
	}
	wantBody(t, f.site("notes.pail.lan", "/api"), 200, "GET /api|")
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "the page of v1")
	wantBody(t, f.do("POST", "notes.pail.lan", "/", nil), http.StatusMethodNotAllowed, "only serves files")
	// The source a container is built from is not a site.
	for _, path := range []string{"/Dockerfile", "/server.js", "/pail.json", "/public/index.html"} {
		if rec := f.site("notes.pail.lan", path); rec.Code != 404 {
			t.Errorf("%s was served: %d %s", path, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(status(), `"containers":[{"name":"api","port":8080,"state":"running"}]`) {
		t.Errorf("status: %s", status())
	}
	wantBody(t, f.api("GET", "/api/v1/pails/notes/output?follow=false", nil), 200, `"text":"booted FROM app:v1","source":"api"`)

	// The next deploy replaces the machine; the data volume passes to it.
	v2, log := f.deploy("notes", tarGz(t, app("v2", "")))
	if v2.State != "ok" || !strings.Contains(log, "stopping the running api, so the new one can take its data") {
		t.Fatalf("second deploy: %+v\n%s", v2, log)
	}
	if got := get("/api/x"); !strings.Contains(got, "FROM app:v2|") || !strings.Contains(got, "boots=2") {
		t.Errorf("after the second deploy: %s", got)
	}
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "the page of v2")
	if n := machines.count(); n != 1 {
		t.Errorf("%d machines running after a deploy, want 1", n)
	}

	// A container that never opens its port fails the deploy, and what was
	// serving comes back.
	deaf, log := f.deploy("notes", tarGz(t, app("v3", "DEAF")))
	if deaf.State != "failed" || !strings.Contains(deaf.Error, "api didn't open port 8080") || !strings.Contains(deaf.Error, "0.0.0.0") {
		t.Fatalf("a container with no port: %+v\n%s", deaf, log)
	}
	if !strings.Contains(log, "still serving "+v2.ID) {
		t.Errorf("log: %s", log)
	}
	eventually(t, "v2 answers again", func() bool { return strings.Contains(get("/api/x"), "FROM app:v2|") })
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "the page of v2")
	// So does one whose Dockerfile doesn't build, or runs nothing.
	if bad, _ := f.deploy("notes", tarGz(t, app("v3", "BROKEN"))); bad.State != "failed" || !strings.Contains(bad.Error, "api didn't build: the build exited with status 1") {
		t.Errorf("a Dockerfile that doesn't build: %+v", bad)
	}
	if bad, _ := f.deploy("notes", tarGz(t, app("v3", "NOCMD"))); bad.State != "failed" || !strings.Contains(bad.Error, "no CMD or ENTRYPOINT") {
		t.Errorf("a Dockerfile with nothing to run: %+v", bad)
	}
	if keys, _ := store.List(context.Background(), "meta/notes/deploys/"); len(keys) == 0 || strings.Contains(strings.Join(keys, " "), deaf.ID+".rootfs") {
		t.Errorf("a failed deploy kept its root filesystem: %v", keys)
	}
	eventually(t, "one machine", func() bool { return machines.count() == 1 })

	// Rolling back boots the root filesystem the older deploy was built with.
	wantBody(t, f.api("POST", "/api/v1/pails/notes/serve", []byte(`{"deploy":"`+v1.ID+`"}`)), 200, `"serving":"`+v1.ID+`"`)
	if got := get("/api/x"); !strings.Contains(got, "FROM app:v1|") || !strings.Contains(got, "PAIL_DEPLOY="+v1.ID) {
		t.Errorf("after rolling back: %s", got)
	}
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "the page of v1")

	// A container that stops is started again.
	machines.mu.Lock()
	crashed := machines.running[0]
	machines.mu.Unlock()
	crashed.end(false)
	eventually(t, "the container came back", func() bool {
		rec := f.site("notes.pail.lan", "/api/x")
		return rec.Code == 200 && strings.Contains(rec.Body.String(), "FROM app:v1|")
	})
	wantBody(t, f.api("GET", "/api/v1/pails/notes/output?follow=false", nil), 200, "api stopped. Starting it again.")

	// Stopping the pail shuts its machine down; starting brings it back.
	wantBody(t, f.api("POST", "/api/v1/pails/notes/stop", nil), 200, `"state":"stopped"`)
	if n := machines.count(); n != 0 {
		t.Errorf("%d machines running in a stopped pail", n)
	}
	wantBody(t, f.site("notes.pail.lan", "/api/x"), http.StatusServiceUnavailable, "notes is off")
	wantBody(t, f.api("POST", "/api/v1/pails/notes/start", nil), 200, `"status":"live"`)
	eventually(t, "the container started", func() bool { return f.site("notes.pail.lan", "/api/x").Code == 200 })

	// Pail restarting brings containers back up, fetching a root filesystem
	// this machine no longer has from storage.
	f.svc.Close()
	os.RemoveAll(filepath.Join(f.dir, "containers"))
	f = newContainerFixture(t, store, machines, f.dir)
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "the page of v1")
	eventually(t, "the container is back after a restart", func() bool {
		rec := f.site("notes.pail.lan", "/api/x")
		if rec.Code != 200 {
			// Until it is up, a visitor is told to come back.
			wantBody(t, rec, http.StatusServiceUnavailable, "api in notes is starting")
			return false
		}
		return strings.Contains(rec.Body.String(), "FROM app:v1|")
	})

	// Redeploy runs the same root filesystem again, as a new deploy.
	rec := f.api("POST", "/api/v1/pails/notes/redeploy", nil)
	wantBody(t, rec, http.StatusAccepted, "redeploy of")
	f.svc.Wait()
	var again deployResult
	json.Unmarshal(rec.Body.Bytes(), &again)
	if got := f.site("notes.pail.lan", "/api/x").Body.String(); !strings.Contains(got, "PAIL_DEPLOY="+again.ID) {
		t.Errorf("after a redeploy: %s", got)
	}

	// Removing the pail stops its machine and deletes what it kept.
	wantBody(t, f.api("DELETE", "/api/v1/pails/notes", nil), http.StatusNoContent, "")
	if n := machines.count(); n != 0 {
		t.Errorf("%d machines running after the pail was removed", n)
	}
	for _, sub := range []string{"containers/notes", "volumes/notes"} {
		if _, err := os.Stat(filepath.Join(f.dir, sub)); !os.IsNotExist(err) {
			t.Errorf("%s outlived its pail", sub)
		}
	}
	if keys, _ := store.List(context.Background(), "meta/notes/"); len(keys) != 0 {
		t.Errorf("left in storage: %v", keys)
	}
}

// A pail's containers are a group: each can reach the others by name, and
// they start together, since one may need another before it answers.
func TestContainersOfAPailReachEachOther(t *testing.T) {
	pails.PortWait = 2 * time.Second
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir())
	groups := func() map[string]string {
		machines.mu.Lock()
		defer machines.mu.Unlock()
		got := map[string]string{}
		for _, m := range machines.running {
			got[m.spec.Hostname] = m.spec.Group + " " + strings.Join(m.spec.Peers, ",")
		}
		return got
	}

	// app sorts before db, and answers only once db is up.
	shop := map[string]string{
		"app/Dockerfile": "FROM app:v1\nNEEDS db",
		"db/Dockerfile":  "FROM db:v1",
		"pail.json": `{
			"containers": {
				"app": {"dockerfile": "./app/Dockerfile", "port": 8080, "memory": "256MB"},
				"db": {"dockerfile": "./db/Dockerfile", "port": 5432, "memory": "256MB"}
			},
			"routes": [{"path": "/*", "to": "container:app"}]
		}`,
	}
	d, log := f.deploy("shop", tarGz(t, shop))
	if d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	wantBody(t, f.site("shop.pail.lan", "/"), 200, "FROM app:v1|GET /|")
	want := "shop." + d.ID + " app,db"
	if got := groups(); got["app"] != want || got["db"] != want {
		t.Errorf("the group each container is in: %v, want %q", got, want)
	}

	// The next deploy's containers are a group of their own.
	next, log := f.deploy("shop", tarGz(t, shop))
	if next.State != "ok" {
		t.Fatalf("second deploy: %+v\n%s", next, log)
	}
	want = "shop." + next.ID + " app,db"
	eventually(t, "the first deploy's containers stop", func() bool { return machines.count() == 2 })
	if got := groups(); got["app"] != want || got["db"] != want {
		t.Errorf("after the second deploy: %v, want %q", got, want)
	}

	// One that fails takes the deploy with it, without waiting out the rest.
	shop["db/Dockerfile"] = "FROM db:v2\nDEAF"
	pails.PortWait = 300 * time.Millisecond
	if bad, log := f.deploy("shop", tarGz(t, shop)); bad.State != "failed" || !strings.Contains(bad.Error, "db didn't open port 5432") {
		t.Errorf("a deploy whose database never answers: %+v\n%s", bad, log)
	}

	// A container by itself is in no group.
	if d, log := f.deploy("solo", tarGz(t, map[string]string{"Dockerfile": "FROM app:solo", "pail.json": `{"containers": {"web": {"port": 3000, "memory": "256MB"}}}`})); d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	if got := groups()["web"]; got != " " {
		t.Errorf("a lone container's group: %q", got)
	}
}

// Only the pails PAIL_ALLOW_LAN names have containers that reach the home
// network.
func TestOnlyNamedPailsReachTheHomeNetwork(t *testing.T) {
	pails.PortWait = 2 * time.Second
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir(), "media")
	solo := tarGz(t, map[string]string{"Dockerfile": "FROM app:solo", "pail.json": `{"containers": {"web": {"port": 3000, "memory": "256MB"}}}`})

	d, log := f.deploy("media", solo)
	if d.State != "ok" || !strings.Contains(log, "media is one of PAIL_ALLOW_LAN's pails") {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	d, log = f.deploy("notes", solo)
	if d.State != "ok" || strings.Contains(log, "PAIL_ALLOW_LAN") {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	machines.mu.Lock()
	defer machines.mu.Unlock()
	for _, m := range machines.running {
		pail := strings.Split(filepath.Base(m.spec.Dir), ".")[0]
		if m.spec.LAN != (pail == "media") {
			t.Errorf("%s's container: LAN is %v", pail, m.spec.LAN)
		}
	}
	if len(machines.running) != 2 {
		t.Errorf("%d machines running, want 2", len(machines.running))
	}
}

// A pail's variables fill in pail.json's ${NAME}. A secret is sealed in the
// store, never comes back from the API, and isn't kept with a deploy.
func TestVariablesFillInPailJSON(t *testing.T) {
	pails.PortWait = 2 * time.Second
	store := storage.NewMemory()
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, store, machines, t.TempDir())
	set := func(key, body string) *httptest.ResponseRecorder {
		return f.api("PUT", "/api/v1/pails/shop/env/"+key, []byte(body))
	}
	stored := func() string {
		keys, _ := store.List(context.Background(), "")
		var all strings.Builder
		for _, key := range keys {
			if strings.HasSuffix(key, ".gz") {
				continue
			}
			b, _ := store.Read(context.Background(), key)
			all.Write(b)
		}
		return all.String()
	}
	shop := func(env string) []byte {
		return tarGz(t, map[string]string{"Dockerfile": "FROM app:v1", "pail.json": `{"containers": {"web": {"port": 3000, "memory": "256MB", "env": ` + env + `}}}`})
	}

	// They can be set before the pail's first deploy, which needs them.
	wantBody(t, set("DB_PASSWORD", `{"value": "hunter2", "secret": true}`), 200, `"name":"DB_PASSWORD","secret":true`)
	wantBody(t, set("GREETING", `{"value": "hello"}`), 200, `"name":"GREETING","value":"hello","secret":false`)
	list := f.api("GET", "/api/v1/pails/shop/env", nil).Body.String()
	if strings.Contains(list, "hunter2") || !strings.Contains(list, `"name":"DB_PASSWORD","secret":true`) || !strings.Contains(list, `"value":"hello"`) {
		t.Errorf("the list: %s", list)
	}
	wantBody(t, set("1BAD", `{"value": "x"}`), 400, "bad_variable")
	wantBody(t, set("EMPTY", `{}`), 400, "bad_request")
	wantBody(t, f.api("GET", "/api/v1/info", nil), 200, `"secrets":true`)

	env := `{"DATABASE_URL": "postgres://app:${DB_PASSWORD}@db/app", "GREETING": "${GREETING}, world", "MODE": "${MODE:-dev}", "SHELL_ONE": "$${HOME}"}`
	d, log := f.deploy("shop", shop(env))
	if d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	got := f.site("shop.pail.lan", "/").Body.String()
	for _, want := range []string{"DATABASE_URL=postgres://app:hunter2@db/app", "GREETING=hello, world", "MODE=dev", "SHELL_ONE=${HOME}"} {
		if !strings.Contains(got, want) {
			t.Errorf("what the container saw lacks %q:\n%s", want, got)
		}
	}
	// The secret is nowhere in the store as itself: not in the variables,
	// and not in what the deploy keeps.
	if strings.Contains(stored(), "hunter2") {
		t.Errorf("the secret is in the store unsealed")
	}
	if strings.Contains(log, "hunter2") {
		t.Errorf("the secret is in the deploy's log:\n%s", log)
	}

	// A variable pail.json uses and the pail lacks fails the deploy, before
	// anything is built, and says how to set it.
	bad, log := f.deploy("shop", shop(`{"KEY": "${API_KEY}"}`))
	if bad.State != "failed" || !strings.Contains(bad.Error, "${API_KEY}") || !strings.Contains(bad.Error, "pail env set shop API_KEY") || strings.Contains(log, "STEP 1/1") {
		t.Errorf("a deploy that lacks a variable: %+v\n%s", bad, log)
	}

	// The next deploy uses a variable as it is then.
	wantBody(t, set("DB_PASSWORD", `{"value": "correct horse", "secret": true}`), 200, `"secret":true`)
	if d, log := f.deploy("shop", shop(env)); d.State != "ok" {
		t.Fatalf("redeploy: %+v\n%s", d, log)
	}
	if got := f.site("shop.pail.lan", "/").Body.String(); !strings.Contains(got, "postgres://app:correct horse@db/app") {
		t.Errorf("after the variable changed: %s", got)
	}

	wantBody(t, f.api("DELETE", "/api/v1/pails/shop/env/GREETING", nil), 204, "")
	wantBody(t, f.api("DELETE", "/api/v1/pails/shop/env/GREETING", nil), 404, "no_variable")

	// Its variables go with the pail.
	wantBody(t, f.api("DELETE", "/api/v1/pails/shop", nil), 204, "")
	if list := f.api("GET", "/api/v1/pails/shop/env", nil).Body.String(); strings.Contains(list, "DB_PASSWORD") {
		t.Errorf("a removed pail's variables: %s", list)
	}
}

// Without a key, a pail can have variables and can't have secrets.
func TestSecretsNeedAKey(t *testing.T) {
	svc := pails.New(pails.Options{Store: storage.NewMemory(), BaseDomain: "pail.lan", MaxDeploys: 3})
	ctx := context.Background()
	if _, err := svc.SetVariable(ctx, "shop", "GREETING", "hello", false); err != nil {
		t.Errorf("a variable: %v", err)
	}
	if _, err := svc.SetVariable(ctx, "shop", "DB_PASSWORD", "hunter2", true); !errors.Is(err, pails.ErrNoSecretKey) {
		t.Errorf("a secret without a key: %v", err)
	}
	if svc.CanKeepSecrets() {
		t.Errorf("a Pail with no key says it can keep secrets")
	}
}

func TestContainerPailJSON(t *testing.T) {
	pails.PortWait = 300 * time.Millisecond
	machines := &fakeMachines{t: t, volumes: map[string]bool{}}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir())
	with := func(pailJSON string) map[string]string {
		return map[string]string{"Dockerfile": "FROM app:solo", "pail.json": pailJSON}
	}

	// One container and no routes: it answers everything.
	d, log := f.deploy("solo", tarGz(t, with(`{"containers": {"web": {"port": 3000, "memory": "256MB"}}}`)))
	if d.State != "ok" || !strings.Contains(log, "(1 vCPU, 256MB)") {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	wantBody(t, f.site("solo.pail.lan", "/"), 200, "FROM app:solo|GET /|")
	wantBody(t, f.site("solo.pail.lan", "/any/path"), 200, "GET /any/path|")
	if n := machines.count(); n != 1 {
		t.Errorf("%d machines", n)
	}
	// With no data volume, the new machine is up before the old one stops.
	if d, log := f.deploy("solo", tarGz(t, with(`{"containers": {"web": {"port": 3000, "cpus": 1, "memory": 128}}}`))); d.State != "ok" || strings.Contains(log, "stopping the running") || !strings.Contains(log, "128MB") {
		t.Errorf("second deploy: %+v\n%s", d, log)
	}
	eventually(t, "the old machine stopped", func() bool { return machines.count() == 1 })

	// Paths no route covers are not found.
	routed, _ := f.deploy("routed", tarGz(t, with(`{"containers": {"web": {"port": 3000, "memory": "64MB"}}, "routes": [{"path": "/app/*", "to": "container:web"}, {"path": "/health", "to": "container:web"}]}`)))
	if routed.State != "ok" {
		t.Fatalf("deploy: %+v", routed)
	}
	wantBody(t, f.site("routed.pail.lan", "/app/x"), 200, "GET /app/x|")
	wantBody(t, f.site("routed.pail.lan", "/health"), 200, "GET /health|")
	wantBody(t, f.site("routed.pail.lan", "/healthy"), 404, "Nothing at /healthy in routed.")
	wantBody(t, f.site("routed.pail.lan", "/"), 404, "Nothing at / in routed.")

	for want, pailJSON := range map[string]string{
		"say how much memory web may use, like \"memory\": \"256MB\"":                                                               `{"containers": {"web": {"port": 80}}}`,
		"web asks for 4GB of memory, and this Pail gives a container at most 2GB. Ask for less, or raise PAIL_MAX_CONTAINER_MEMORY": `{"containers": {"web": {"port": 80, "memory": "4GB"}}}`,
		"asks for less memory than a microVM boots in":                                                                              `{"containers": {"web": {"port": 80, "memory": "8MB"}}}`,
		"has both an image and a dockerfile":                                                                                        `{"containers": {"web": {"port": 80, "memory": "64MB", "image": "nginx", "dockerfile": "Dockerfile"}}}`,
		"image must name an image in a registry":                                                                                    `{"containers": {"web": {"port": 80, "memory": "64MB", "image": "Not An Image"}}}`,
		"web's image didn't arrive: can't pull nowhere/nothing: no such image":                                                      `{"containers": {"web": {"port": 80, "memory": "64MB", "image": "nowhere/nothing"}}}`,
		"say which port web listens on":                                                                                             `{"containers": {"web": {}}}`,
		"port must be between 2 and 65535":                                                                                          `{"containers": {"web": {"port": 1}}}`,
		"memory must be a size like 512MB":                                                                                          `{"containers": {"web": {"port": 80, "memory": "lots"}}}`,
		"asks for 999 cpus":                                                                                                         `{"containers": {"web": {"port": 80, "cpus": 999}}}`,
		"data must be a folder's full path":                                                                                         `{"containers": {"web": {"port": 80, "memory": "64MB", "data": "data"}}}`,
		"can't keep its data at /etc":                                                                                               `{"containers": {"web": {"port": 80, "memory": "64MB", "data": "/etc"}}}`,
		"container names are lowercase":                                                                                             `{"containers": {"Web App": {"port": 80, "memory": "64MB"}}}`,
		"declares no container by that name":                                                                                        `{"containers": {"web": {"port": 80, "memory": "64MB"}}, "routes": [{"path": "/*", "to": "container:api"}]}`,
		"doesn't say which folder holds the files":                                                                                  `{"containers": {"web": {"port": 80, "memory": "64MB"}}, "routes": [{"path": "/*", "to": "static"}]}`,
		"needs routes to say which paths go where":                                                                                  `{"containers": {"web": {"port": 80, "memory": "64MB"}, "api": {"port": 81, "memory": "64MB"}}}`,
		"the upload has no such file":                                                                                               `{"containers": {"web": {"port": 80, "memory": "64MB", "dockerfile": "./docker/Dockerfile"}}}`,
		"A route goes to \"static\", \"function:<name>\" or \"container:<name>\"":                                                   `{"containers": {"web": {"port": 80, "memory": "64MB"}}, "routes": [{"path": "/*", "to": "elsewhere"}]}`,
	} {
		if d, _ := f.deploy("wrong", tarGz(t, with(pailJSON))); d.State != "failed" || !strings.Contains(d.Error, want) {
			t.Errorf("%s: got %+v, want an error containing %q", pailJSON, d, want)
		}
	}

	// Where microVMs can't run, a deploy with containers is refused, saying why.
	machines.down = "this machine has no /dev/kvm"
	if d, _ := f.deploy("nowhere", tarGz(t, with(`{"containers": {"web": {"port": 80, "memory": "64MB"}}}`))); d.State != "failed" || !strings.Contains(d.Error, "this Pail can't run it: this machine has no /dev/kvm") {
		t.Errorf("with no microVMs: %+v", d)
	}
}

// A container can run an image from a registry as it is, with no Dockerfile
// and nothing else in the upload.
func TestContainersFromARegistry(t *testing.T) {
	pails.PortWait = 300 * time.Millisecond
	machines := &fakeMachines{t: t, volumes: map[string]bool{}, digests: map[string]string{"nginx:1.27": "sha256:one"}}
	f := newContainerFixture(t, storage.NewMemory(), machines, t.TempDir())
	command := ""
	upload := func() []byte {
		return tarGz(t, map[string]string{"pail.json": `{"containers": {"web": {"image": "nginx:1.27", "port": 80, "memory": "64MB"` + command + `}}}`})
	}
	get := func() string { return f.site("web.pail.lan", "/").Body.String() }

	v1, log := f.deploy("web", upload())
	if v1.State != "ok" || !strings.Contains(log, "pulling nginx:1.27 for web") || !strings.Contains(log, "starting web in a microVM (1 vCPU, 64MB)") {
		t.Fatalf("deploy: %+v\n%s", v1, log)
	}
	if got := get(); !strings.Contains(got, "IMAGE nginx:1.27@sha256:one|GET /|") || !strings.Contains(got, "argv=[/entry run]") {
		t.Errorf("what answered: %s", got)
	}

	// The same image again is not fetched again.
	v2, log := f.deploy("web", upload())
	if v2.State != "ok" || !strings.Contains(log, "nginx:1.27 hasn't changed since "+v1.ID) || machines.pulls != 1 {
		t.Fatalf("second deploy: %+v, %d pulls\n%s", v2, machines.pulls, log)
	}
	if got := get(); !strings.Contains(got, "@sha256:one|") || !strings.Contains(got, "PAIL_DEPLOY="+v2.ID) {
		t.Errorf("after the second deploy: %s", got)
	}

	// When the tag moves, a redeploy follows it.
	machines.mu.Lock()
	machines.digests["nginx:1.27"] = "sha256:two"
	machines.mu.Unlock()
	rec := f.api("POST", "/api/v1/pails/web/redeploy", nil)
	wantBody(t, rec, http.StatusAccepted, "redeploy of")
	f.svc.Wait()
	if got := get(); !strings.Contains(got, "@sha256:two|") || machines.pulls != 2 {
		t.Errorf("after the tag moved: %d pulls, %s", machines.pulls, got)
	}
	// And a rollback goes back to exactly what that deploy ran.
	wantBody(t, f.api("POST", "/api/v1/pails/web/serve", []byte(`{"deploy":"`+v1.ID+`"}`)), 200, `"serving":"`+v1.ID+`"`)
	if got := get(); !strings.Contains(got, "@sha256:one|") {
		t.Errorf("after rolling back: %s", got)
	}

	// A command runs in place of the image's CMD, behind its ENTRYPOINT, and
	// takes effect even when the image itself needn't be fetched again.
	machines.mu.Lock()
	machines.digests["nginx:1.27"] = "sha256:one"
	machines.mu.Unlock()
	command = `, "command": ["serve", "--port", "80"]`
	pulls := machines.pulls
	if d, log := f.deploy("web", upload()); d.State != "ok" || machines.pulls != pulls {
		t.Fatalf("deploy with a command: %+v, %d pulls\n%s", d, machines.pulls-pulls, log)
	}
	if got := get(); !strings.Contains(got, "argv=[/entry serve --port 80]") {
		t.Errorf("with a command: %s", got)
	}
	command = `, "command": "serve --port 80"`
	if bad, _ := f.deploy("web", upload()); bad.State != "failed" || !strings.Contains(bad.Error, "web's command must be a list of words") {
		t.Errorf("a command that isn't a list: %+v", bad)
	}

	// An image that says nothing about what to run can't be run, unless
	// pail.json says.
	machines.mu.Lock()
	machines.digests["nginx:1.27"] = "sha256:nocmd"
	machines.mu.Unlock()
	command = ""
	if bad, _ := f.deploy("web", upload()); bad.State != "failed" || !strings.Contains(bad.Error, "nginx:1.27 has no CMD or ENTRYPOINT") {
		t.Errorf("an image with nothing to run: %+v", bad)
	}
	if got := get(); !strings.Contains(got, "argv=[/entry serve --port 80]") {
		t.Errorf("after a failed deploy: %s", got)
	}
	command = `, "command": ["/bin/app"]`
	if d, _ := f.deploy("web", upload()); d.State != "ok" || !strings.Contains(get(), "argv=[/bin/app]") {
		t.Errorf("an image with nothing to run, given a command: %+v, %s", d, get())
	}
}

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/chrisdmacrae/pail/internal/certs"
	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/githost"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/storage"
)

const token = "test-token"

// testUI stands in for the built web UI.
var testUI = fstest.MapFS{
	"index.html":        {Data: []byte("<title>Pail</title>")},
	"favicon.svg":       {Data: []byte("<svg/>")},
	"assets/app-abc.js": {Data: []byte("console.log('pail')")},
}

type fixture struct {
	t     *testing.T
	store *storage.Memory
	svc   *pails.Service
	srv   *Server
}

func newFixture(t *testing.T, store *storage.Memory) *fixture {
	t.Helper()
	conns, err := githost.LoadConnections(context.Background(), store, githost.DefaultClient())
	if err != nil {
		t.Fatal(err)
	}
	return newFixtureWith(t, store, conns)
}

func newFixtureWith(t *testing.T, store *storage.Memory, conns *githost.Connections) *fixture {
	t.Helper()
	cfg := config.Config{Token: token, BaseDomain: "pail.lan", MaxUploadSize: 1 << 20, MaxDeploys: 3, MaxFunctionMemory: 1 << 30}
	cfg.ACME = config.ACME{DNSProvider: "test", DNSToken: "test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := pails.New(pails.Options{Store: store, BaseDomain: cfg.BaseDomain, MaxDeploys: cfg.MaxDeploys, MaxUnpackedSize: 10 << 20, Logger: logger})
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: store, svc: svc, srv: New(Options{Config: cfg, Pails: svc, UI: testUI, Git: conns, Logger: logger, Version: "test"})}
}

func (f *fixture) do(method, host, target string, body []byte, header ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) api(method, target string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(method, "pail.lan", target, body, "Authorization", "Bearer "+token)
}

func (f *fixture) site(host, target string, header ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do("GET", host, target, nil, header...)
}

type deployResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error"`
	URL   string `json:"url"`
}

// deploy uploads an archive and follows its log to the end.
func (f *fixture) deploy(name string, archive []byte) (deployResult, string) {
	f.t.Helper()
	rec := f.api("POST", "/api/v1/pails/"+name+"/deploys?source=cli", archive)
	if rec.Code != http.StatusAccepted {
		f.t.Fatalf("deploy %s: %d %s", name, rec.Code, rec.Body)
	}
	var started deployResult
	json.Unmarshal(rec.Body.Bytes(), &started)

	log := f.api("GET", "/api/v1/pails/"+name+"/deploys/"+started.ID+"/log", nil)
	if log.Code != http.StatusOK {
		f.t.Fatalf("log: %d %s", log.Code, log.Body)
	}
	_, done, ok := strings.Cut(log.Body.String(), "event: done\ndata: ")
	if !ok {
		f.t.Fatalf("log never finished:\n%s", log.Body)
	}
	var d deployResult
	if err := json.Unmarshal([]byte(done), &d); err != nil {
		f.t.Fatal(err)
	}
	f.svc.Wait()
	return d, log.Body.String()
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code || !strings.Contains(rec.Body.String(), body) {
		t.Fatalf("got %d %q, want %d containing %q", rec.Code, rec.Body, code, body)
	}
}

func TestTokenIsRequired(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	if rec := f.do("GET", "pail.lan", "/api/v1/info", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}
	if rec := f.do("GET", "pail.lan", "/api/v1/info", nil, "Authorization", "Bearer nope"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
	// The API answers by IP too, as a profile URL like http://10.0.20.15:8080 needs.
	rec := f.do("GET", "10.0.20.15:8080", "/api/v1/info", nil, "Authorization", "Bearer "+token)
	wantBody(t, rec, http.StatusOK, `"base_domain":"pail.lan"`)
}

func TestDeployAndServe(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	d, log := f.deploy("blog", tarGz(t, map[string]string{
		"index.html":      "<h1>blog</h1>",
		"about.html":      "about",
		"docs/index.html": "docs",
		"app.js":          "console.log(1)",
		".DS_Store":       "junk",
		"._index.html":    "junk",
	}))
	if d.State != "ok" || d.URL != "http://blog.pail.lan" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	for _, want := range []string{"unpacking 4 files · found index.html", "swapping blog.pail.lan to " + d.ID, "✓ live at http://blog.pail.lan"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	wantBody(t, f.site("blog.pail.lan", "/"), 200, "<h1>blog</h1>")
	wantBody(t, f.site("BLOG.pail.lan:80", "/index.html"), 200, "<h1>blog</h1>")
	wantBody(t, f.site("blog.pail.lan", "/about"), 200, "about")
	wantBody(t, f.site("blog.pail.lan", "/docs/"), 200, "docs")
	wantBody(t, f.site("blog.pail.lan", "/nope"), 404, "This is Pail on pail.lan.")
	wantBody(t, f.site("blog.pail.lan", "/.DS_Store"), 404, "")
	wantBody(t, f.site("other.pail.lan", "/"), 404, "No pail called other.")
	wantBody(t, f.site("example.com", "/"), 404, "Nothing is hosted at example.com.")

	if rec := f.site("blog.pail.lan", "/docs?x=1"); rec.Code != 301 || rec.Header().Get("Location") != "/docs/?x=1" {
		t.Errorf("folder redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	js := f.site("blog.pail.lan", "/app.js")
	if ct := js.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("app.js content type %q", ct)
	}
	if rec := f.site("blog.pail.lan", "/app.js", "If-None-Match", js.Header().Get("ETag")); rec.Code != 304 {
		t.Errorf("revalidation: got %d", rec.Code)
	}
	if rec := f.site("blog.pail.lan", "/app.js", "Range", "bytes=0-6"); rec.Code != 206 || rec.Body.String() != "console" {
		t.Errorf("range: %d %q", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "blog.pail.lan", "/", nil); rec.Code != 405 {
		t.Errorf("POST to a site: got %d", rec.Code)
	}

	list := f.api("GET", "/api/v1/pails", nil)
	for _, want := range []string{`"name":"blog"`, `"status":"live"`, `"source":"cli"`, `"serving":"` + d.ID + `"`, `"url":"http://blog.pail.lan"`} {
		if !strings.Contains(list.Body.String(), want) {
			t.Errorf("list lacks %s: %s", want, list.Body)
		}
	}
}

func TestFailedDeployKeepsThePreviousOneServing(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	good, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": "v1"}))

	bad, log := f.deploy("blog", tarGz(t, map[string]string{"readme.txt": "no site here"}))
	if bad.State != "failed" || !strings.Contains(bad.Error, "No index.html") {
		t.Fatalf("deploy: %+v", bad)
	}
	if !strings.Contains(log, "still serving "+good.ID) {
		t.Errorf("log doesn't say what's still serving:\n%s", log)
	}
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")

	p := f.api("GET", "/api/v1/pails/blog", nil).Body.String()
	if !strings.Contains(p, `"status":"failed"`) || !strings.Contains(p, `"serving":"`+good.ID+`"`) {
		t.Errorf("pail: %s", p)
	}
	if keys, _ := f.store.List(context.Background(), "pails/blog/deploys/"+bad.ID+"/"); len(keys) != 0 {
		t.Errorf("failed deploy left files: %v", keys)
	}

	climbing, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": "v3", "../../escape": "x"}))
	if climbing.State != "failed" {
		t.Errorf("archive with ../ deployed: %+v", climbing)
	}
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")
}

func TestZipFolderWithPailJSON(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	d, log := f.deploy("dash", zipOf(t, map[string]string{
		"home-dashboard/pail.json":        `{"static": "./build", "routes": [{"path": "/*", "to": "static", "fallback": "index.html"}]}`,
		"home-dashboard/build/index.html": "app shell",
		"home-dashboard/build/main.css":   "body{}",
		"home-dashboard/src/secret.txt":   "not served",
		"__MACOSX/home-dashboard/._x":     "junk",
	}))
	if d.State != "ok" {
		t.Fatalf("deploy: %+v\n%s", d, log)
	}
	wantBody(t, f.site("dash.pail.lan", "/"), 200, "app shell")
	wantBody(t, f.site("dash.pail.lan", "/main.css"), 200, "body{}")
	wantBody(t, f.site("dash.pail.lan", "/settings/profile"), 200, "app shell")
	if rec := f.site("dash.pail.lan", "/pail.json"); rec.Body.String() != "app shell" {
		t.Errorf("pail.json was served: %q", rec.Body)
	}

	withFn, _ := f.deploy("dash", tarGz(t, map[string]string{
		"pail.json":  `{"functions": {"api": {"src": "./fn/api"}}}`,
		"index.html": "x",
	}))
	if withFn.State != "failed" || !strings.Contains(withFn.Error, "functions or containers") {
		t.Errorf("functions deploy: %+v", withFn)
	}
}

func TestUploadsPailRefuses(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	cases := []struct {
		target string
		body   []byte
		code   int
	}{
		{"/api/v1/pails/blog/deploys", bytes.Repeat([]byte("x"), 2<<20), http.StatusRequestEntityTooLarge},
		{"/api/v1/pails/blog/deploys", []byte("<html>not an archive</html>"), http.StatusUnsupportedMediaType},
		{"/api/v1/pails/blog/deploys", nil, http.StatusBadRequest},
		{"/api/v1/pails/Not_A_Name/deploys", tarGz(t, map[string]string{"index.html": "x"}), http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := f.api("POST", c.target, c.body); rec.Code != c.code {
			t.Errorf("%s (%d bytes): got %d %s, want %d", c.target, len(c.body), rec.Code, rec.Body, c.code)
		}
	}
	if list := f.svc.List(); len(list) != 0 {
		t.Errorf("refused uploads made pails: %+v", list)
	}
}

func TestOldDeploysArePruned(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	var ids []string
	for _, v := range []string{"v1", "v2", "v3", "v4", "v5"} {
		d, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": v}))
		ids = append(ids, d.ID)
	}
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v5")

	ctx := context.Background()
	for i, id := range ids {
		files, _ := f.store.List(ctx, "pails/blog/deploys/"+id+"/")
		meta, _ := f.store.List(ctx, "meta/blog/deploys/"+id+".")
		kept := i >= 2 // MaxDeploys is 3
		if (len(files) > 0) != kept || (len(meta) > 0) != kept {
			t.Errorf("deploy %d: kept=%v but files=%v meta=%v", i+1, kept, files, meta)
		}
	}
	if rec := f.api("GET", "/api/v1/pails/blog/deploys/"+ids[0]+"/log", nil); rec.Code != http.StatusNotFound {
		t.Errorf("log of a pruned deploy: got %d", rec.Code)
	}
}

func TestRestartAndRemove(t *testing.T) {
	store := storage.NewMemory()
	first := newFixture(t, store)
	d, _ := first.deploy("blog", tarGz(t, map[string]string{"index.html": "v1"}))

	// A second Pail on the same storage picks up where the first left off.
	f := newFixture(t, store)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")
	log := f.api("GET", "/api/v1/pails/blog/deploys/"+d.ID+"/log", nil)
	wantBody(t, log, 200, "✓ live at http://blog.pail.lan")

	if rec := f.api("DELETE", "/api/v1/pails/blog", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: got %d %s", rec.Code, rec.Body)
	}
	wantBody(t, f.site("blog.pail.lan", "/"), 404, "No pail called blog.")
	if rec := f.api("DELETE", "/api/v1/pails/blog", nil); rec.Code != http.StatusNotFound {
		t.Errorf("removing it twice: got %d", rec.Code)
	}
	if keys, _ := store.List(context.Background(), ""); len(keys) != 0 {
		t.Errorf("remove left objects: %v", keys)
	}
}

func TestHistoryAndRollback(t *testing.T) {
	store := storage.NewMemory()
	f := newFixture(t, store)
	serve := func(id string) *httptest.ResponseRecorder {
		return f.api("POST", "/api/v1/pails/blog/serve", []byte(`{"deploy":"`+id+`"}`))
	}
	status := func() string { return f.api("GET", "/api/v1/pails/blog", nil).Body.String() }

	v1, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": "v1"}))
	v2, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": "v2"}))
	bad, _ := f.deploy("blog", tarGz(t, map[string]string{"nope.txt": "x"}))

	var history struct {
		Serving string
		Deploys []struct {
			ID, State string
			Serving   bool
		}
	}
	json.Unmarshal(f.api("GET", "/api/v1/pails/blog/deploys", nil).Body.Bytes(), &history)
	if len(history.Deploys) != 3 || history.Serving != v2.ID {
		t.Fatalf("history: %+v", history)
	}
	for i, want := range []struct {
		id, state string
		serving   bool
	}{{bad.ID, "failed", false}, {v2.ID, "ok", true}, {v1.ID, "ok", false}} {
		if d := history.Deploys[i]; d.ID != want.id || d.State != want.state || d.Serving != want.serving {
			t.Errorf("deploy %d: got %+v, want %+v", i, d, want)
		}
	}
	if !strings.Contains(status(), `"status":"failed"`) {
		t.Errorf("before rollback: %s", status())
	}

	// Rolling back is choosing what to serve: the pail is live again.
	wantBody(t, serve(v1.ID), 200, `"serving":"`+v1.ID+`"`)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")
	if !strings.Contains(status(), `"status":"live"`) {
		t.Errorf("after rollback: %s", status())
	}
	wantBody(t, serve(v1.ID), 200, `"serving":"`+v1.ID+`"`) // again: nothing to do

	wantBody(t, serve(bad.ID), http.StatusConflict, "didn't finish")
	wantBody(t, serve("0000000"), http.StatusNotFound, "no deploy 0000000")
	wantBody(t, f.api("POST", "/api/v1/pails/blog/serve", []byte(`{}`)), http.StatusBadRequest, "which deploy")
	wantBody(t, f.api("POST", "/api/v1/pails/nope/serve", []byte(`{"deploy":"x"}`)), http.StatusNotFound, "No pail called nope")
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")

	// The pointer is in storage, so a restart serves the same deploy.
	f = newFixture(t, store)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")

	// Failed deploys don't count against the good ones: any number of them
	// leaves every rollback target in place. They have a limit of their own.
	for range 3 {
		f.deploy("blog", tarGz(t, map[string]string{"nope.txt": "x"}))
	}
	json.Unmarshal(f.api("GET", "/api/v1/pails/blog/deploys", nil).Body.Bytes(), &history)
	states := map[string]int{}
	for _, d := range history.Deploys {
		states[d.State]++
	}
	if states["ok"] != 2 || states["failed"] != 3 { // MaxDeploys is 3; four have failed
		t.Errorf("kept deploys: %v", states)
	}
	wantBody(t, f.api("GET", "/api/v1/pails/blog/deploys/"+bad.ID+"/log", nil), http.StatusNotFound, "no deploy")
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v1")
	wantBody(t, serve(v2.ID), 200, `"serving":"`+v2.ID+`"`)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v2")

	// Good deploys past the limit go oldest first, but never the one served.
	serve(v1.ID)
	for _, v := range []string{"v3", "v4", "v5"} {
		f.deploy("blog", tarGz(t, map[string]string{"index.html": v}))
	}
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v5")
	wantBody(t, serve(v1.ID), http.StatusNotFound, "no deploy")
	wantBody(t, serve(v2.ID), http.StatusNotFound, "no deploy")
}

func TestStopStartAndRedeploy(t *testing.T) {
	store := storage.NewMemory()
	f := newFixture(t, store)
	post := func(action string) *httptest.ResponseRecorder {
		return f.api("POST", "/api/v1/pails/blog/"+action, nil)
	}
	f.deploy("blog", tarGz(t, map[string]string{"index.html": "v1", "pail.json": `{"name": "blog"}`}))

	wantBody(t, post("stop"), 200, `"status":"off"`)
	wantBody(t, f.site("blog.pail.lan", "/"), http.StatusServiceUnavailable, "blog is off.")
	wantBody(t, post("stop"), 200, `"status":"off"`)

	// A deploy to a stopped pail lands, but the pail stays off, across a
	// restart too, until it is started.
	d, _ := f.deploy("blog", tarGz(t, map[string]string{"index.html": "v2"}))
	if d.State != "ok" {
		t.Fatalf("deploy to a stopped pail: %+v", d)
	}
	f = newFixture(t, store)
	wantBody(t, f.api("GET", "/api/v1/pails/blog", nil), 200, `"status":"off"`)
	wantBody(t, f.site("blog.pail.lan", "/"), http.StatusServiceUnavailable, "blog is off.")
	wantBody(t, post("start"), 200, `"status":"live"`)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v2")

	// Redeploy copies the latest good deploy into a new one.
	rec := post("redeploy")
	wantBody(t, rec, http.StatusAccepted, `"label":"redeploy of `+d.ID+`"`)
	f.svc.Wait()
	var again deployResult
	json.Unmarshal(rec.Body.Bytes(), &again)
	wantBody(t, f.api("GET", "/api/v1/pails/blog", nil), 200, `"serving":"`+again.ID+`"`)
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "v2")
	if keys, _ := store.List(context.Background(), "pails/blog/deploys/"+again.ID+"/"); len(keys) != 1 {
		t.Errorf("redeploy copied %v", keys)
	}

	// Nothing good to copy: refused before a deploy is recorded.
	f.deploy("empty", tarGz(t, map[string]string{"nope.txt": "x"}))
	wantBody(t, f.api("POST", "/api/v1/pails/empty/redeploy", nil), http.StatusConflict, "no finished deploy")
	wantBody(t, f.api("POST", "/api/v1/pails/nope/redeploy", nil), http.StatusNotFound, "No pail called nope")
	wantBody(t, f.api("POST", "/api/v1/pails/nope/stop", nil), http.StatusNotFound, "No pail called nope")
}

func TestWebUI(t *testing.T) {
	f := newFixture(t, storage.NewMemory())

	// The page is public; what it shows comes from the API, which isn't.
	for _, target := range []string{"/", "/new", "/pails/blog"} {
		rec := f.site("pail.lan", target)
		wantBody(t, rec, 200, "<title>Pail</title>")
		if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: headers %v", target, rec.Header())
		}
	}
	js := f.site("pail.lan", "/assets/app-abc.js")
	wantBody(t, js, 200, "console.log")
	if cc := js.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache: %q", cc)
	}
	wantBody(t, f.site("pail.lan", "/favicon.svg"), 200, "<svg/>")
	wantBody(t, f.site("pail.lan", "/assets/gone.js"), 404, "This is Pail on pail.lan.")
	wantBody(t, f.site("10.0.20.15:8080", "/"), 200, "<title>Pail</title>")
	if rec := f.do("POST", "pail.lan", "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: got %d", rec.Code)
	}

	// The UI never shadows the API, and a pail's host never serves the UI.
	wantBody(t, f.site("pail.lan", "/api/v1/info"), http.StatusUnauthorized, "token")
	wantBody(t, f.site("blog.pail.lan", "/"), 404, "No pail called blog.")

	// A server built without the UI says so instead.
	f.srv = New(Options{Config: config.Config{Token: token, BaseDomain: "pail.lan"}, Pails: f.svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	wantBody(t, f.site("pail.lan", "/"), 200, "This build has no web UI")
	wantBody(t, f.site("pail.lan", "/new"), 404, "This is Pail on pail.lan.")
}

func TestCustomHostnames(t *testing.T) {
	store := storage.NewMemory()
	f := newFixture(t, store)
	// A real listener, and every name "resolving" to it, stands in for DNS
	// that points at this Pail.
	ts := httptest.NewServer(f.srv)
	defer ts.Close()
	f.srv.DialProbesAt(ts.Listener.Addr().String())
	add := func(pail, host string) *httptest.ResponseRecorder {
		return f.api("POST", "/api/v1/pails/"+pail+"/hosts", []byte(`{"host":"`+host+`"}`))
	}

	f.deploy("blog", tarGz(t, map[string]string{"index.html": "blog"}))
	f.deploy("recipes", tarGz(t, map[string]string{"index.html": "recipes"}))
	wantBody(t, f.site("blog.home.example", "/"), 404, "Nothing is hosted at blog.home.example.")

	wantBody(t, add("blog", "blog.home.example"), http.StatusCreated, `"points_here":true`)
	wantBody(t, f.site("blog.home.example", "/"), 200, "blog")
	wantBody(t, f.site("blog.pail.lan", "/"), 200, "blog")
	// Pasted with a scheme, a path and capitals, it's the same hostname.
	wantBody(t, add("blog", "HTTPS://Blog.Home.Example/about"), http.StatusCreated, `"host":"blog.home.example"`)
	wantBody(t, f.api("GET", "/api/v1/pails/blog", nil), 200, `"hosts":["blog.home.example"]`)

	var listed struct {
		Hosts []struct {
			Host       string
			Default    bool
			PointsHere bool `json:"points_here"`
		}
	}
	json.Unmarshal(f.api("GET", "/api/v1/pails/blog/hosts", nil).Body.Bytes(), &listed)
	if len(listed.Hosts) != 2 || listed.Hosts[0].Host != "blog.pail.lan" || !listed.Hosts[0].Default ||
		listed.Hosts[1].Host != "blog.home.example" || listed.Hosts[1].Default || !listed.Hosts[1].PointsHere {
		t.Errorf("hosts: %+v", listed.Hosts)
	}

	wantBody(t, add("recipes", "blog.home.example"), http.StatusConflict, "already belongs to blog")
	wantBody(t, add("recipes", "nope"), http.StatusBadRequest, "isn't a hostname")
	wantBody(t, add("recipes", "10.0.0.5"), http.StatusBadRequest, "isn't a hostname")
	wantBody(t, add("recipes", "other.pail.lan"), http.StatusBadRequest, "Names under pail.lan belong to pails")
	wantBody(t, add("nope", "x.home.example"), http.StatusNotFound, "No pail called nope")

	wantBody(t, f.api("GET", "/api/v1/check", nil), 200, `"points_here":true`)
	// Only a check in flight is answered; the path is nothing otherwise.
	wantBody(t, f.site("blog.pail.lan", "/.well-known/pail/guess"), 404, "")

	// Hostnames are in storage: a restart still routes them.
	f = newFixture(t, store)
	wantBody(t, f.site("blog.home.example", "/"), 200, "blog")
	// With no DNS pointing here, the same hostname is listed as not there yet.
	wantBody(t, add("recipes", "recipes.pail-test.invalid"), http.StatusCreated, `"points_here":false`)
	wantBody(t, f.api("GET", "/api/v1/check", nil), 200, "don't reach this Pail yet")

	del := f.api("DELETE", "/api/v1/pails/blog/hosts/blog.home.example", nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("remove host: %d %s", del.Code, del.Body)
	}
	wantBody(t, f.site("blog.home.example", "/"), 404, "Nothing is hosted")
	wantBody(t, f.api("DELETE", "/api/v1/pails/blog/hosts/blog.home.example", nil), http.StatusNotFound, "blog has no hostname")

	// Removing a pail frees its hostnames for another.
	f.api("DELETE", "/api/v1/pails/recipes", nil)
	wantBody(t, f.site("recipes.pail-test.invalid", "/"), 404, "Nothing is hosted")
	wantBody(t, add("blog", "recipes.pail-test.invalid"), http.StatusCreated, "recipes.pail-test.invalid")

	// Without Let's Encrypt set up, hostnames are refused, and info says so.
	plain := New(Options{Config: config.Config{Token: token, BaseDomain: "pail.lan"}, Pails: f.svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	f.srv = plain
	wantBody(t, add("blog", "x.home.example"), http.StatusConflict, "PAIL_ACME_DNS_PROVIDER and PAIL_ACME_DNS_TOKEN")
	wantBody(t, f.api("GET", "/api/v1/info", nil), 200, `"custom_hostnames":false`)
}

func TestHTTPS(t *testing.T) {
	store := storage.NewMemory()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := pails.New(pails.Options{Store: store, BaseDomain: "pail.lan", MaxDeploys: 3, Logger: logger})
	manager, err := certs.New(context.Background(), certs.Options{Store: store, BaseDomain: "pail.lan", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Token: token, BaseDomain: "pail.lan", MaxUploadSize: 1 << 20, MaxDeploys: 3, Listen: ":80", ListenTLS: ":8443"}
	srv := New(Options{Config: cfg, Pails: svc, UI: testUI, Certs: manager, Logger: logger, Version: "test"})
	f := &fixture{t: t, store: store, svc: svc, srv: srv}

	secure := httptest.NewUnstartedServer(srv)
	secure.TLS = &tls.Config{GetCertificate: manager.GetCertificate}
	secure.StartTLS()
	defer secure.Close()

	// A device that trusts Pail's root, with every name resolving to Pail.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(manager.RootPEM())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, secure.Listener.Addr().String())
		},
	}}
	get := func(url string, header ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", url, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	f.deploy("blog", tarGz(t, map[string]string{"index.html": "over https"}))
	if code, body := get("https://blog.pail.lan/"); code != 200 || body != "over https" {
		t.Errorf("site over https: %d %q", code, body)
	}
	if code, body := get("https://pail.lan/api/v1/info", "Authorization", "Bearer "+token); code != 200 || !strings.Contains(body, `"tls":"internal"`) {
		t.Errorf("info over https: %d %s", code, body)
	}
	if code, body := get("https://pail.lan/api/v1/pails", "Authorization", "Bearer "+token); code != 200 || !strings.Contains(body, `"url":"https://blog.pail.lan"`) {
		t.Errorf("pail urls should be https: %d %s", code, body)
	}
	if code, body := get("https://pail.lan/ca.crt"); code != 200 || body != string(manager.RootPEM()) {
		t.Errorf("ca.crt over https: %d", code)
	}
	// A device that hasn't trusted the root is refused by its own TLS.
	if _, err := http.Get(secure.URL); err == nil {
		t.Error("an untrusting client connected")
	} else if unknown := (x509.UnknownAuthorityError{}); !errors.As(err, &unknown) {
		t.Errorf("untrusting client: %v", err)
	}

	// The plain listener: the root certificate, the self-check, and a
	// redirect to HTTPS for everything else.
	plain := func(host, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.Plain().ServeHTTP(rec, req)
		return rec
	}
	wantBody(t, plain("pail.lan", "/ca.crt"), 200, "BEGIN CERTIFICATE")
	for host, want := range map[string]string{
		"blog.pail.lan":    "https://blog.pail.lan:8443/docs/?page=2",
		"blog.pail.lan:80": "https://blog.pail.lan:8443/docs/?page=2",
		"pail.lan":         "https://pail.lan:8443/docs/?page=2",
	} {
		rec := plain(host, "/docs/?page=2")
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d to %q, want %q", host, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	if rec := plain("blog.pail.lan", "/ca.crt"); rec.Code != http.StatusPermanentRedirect {
		t.Errorf("a pail's own /ca.crt on plain http: %d", rec.Code)
	}

	// The self-check goes to the plain listener, whatever port the API call
	// came in on.
	plainSrv := httptest.NewServer(srv.Plain())
	defer plainSrv.Close()
	srv.DialProbesAt(plainSrv.Listener.Addr().String())
	if code, body := get("https://pail.lan/api/v1/check", "Authorization", "Bearer "+token); code != 200 || !strings.Contains(body, `"points_here":true`) {
		t.Errorf("self-check with https on: %d %s", code, body)
	}

	// On Pail's own authority there are no custom hostnames.
	wantBody(t, f.api("POST", "/api/v1/pails/blog/hosts", []byte(`{"host":"blog.home.example"}`)), http.StatusConflict, "PAIL_ACME_DNS_PROVIDER")
}

// fakeCerts stands in for Let's Encrypt: it issues for any hostname but the
// ones it is told the DNS token can't speak for.
type fakeCerts struct {
	refuse string
	have   map[string]bool
}

func (c *fakeCerts) Mode() string    { return "acme" }
func (c *fakeCerts) RootPEM() []byte { return nil }
func (c *fakeCerts) AddHost(_ context.Context, host string) error {
	if host == c.refuse {
		return errors.New("zone not found")
	}
	c.have[host] = true
	return nil
}
func (c *fakeCerts) RemoveHost(_ context.Context, host string) { delete(c.have, host) }

func TestCustomHostnamesGetCertificates(t *testing.T) {
	f := newFixture(t, storage.NewMemory())
	fake := &fakeCerts{refuse: "blog.not-mine.example", have: map[string]bool{}}
	cfg := config.Config{Token: token, BaseDomain: "pail.example", MaxUploadSize: 1 << 20, MaxDeploys: 3}
	cfg.ACME = config.ACME{DNSProvider: "test", DNSToken: "test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := pails.New(pails.Options{Store: f.store, BaseDomain: cfg.BaseDomain, MaxDeploys: 3, Logger: logger})
	f.svc, f.srv = svc, New(Options{Config: cfg, Pails: svc, Certs: fake, Logger: logger, Version: "test"})
	api := func(method, target, body string) *httptest.ResponseRecorder {
		return f.do(method, "pail.example", target, []byte(body), "Authorization", "Bearer "+token)
	}
	rec := api("POST", "/api/v1/pails/blog/deploys", string(tarGz(t, map[string]string{"index.html": "blog"})))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	svc.Wait()

	wantBody(t, api("GET", "/api/v1/info", ""), 200, `"tls":"acme"`)
	wantBody(t, api("POST", "/api/v1/pails/blog/hosts", `{"host":"blog.home.example"}`), http.StatusCreated, "blog.home.example")
	if !fake.have["blog.home.example"] {
		t.Error("the hostname was added without a certificate")
	}

	// A hostname Let's Encrypt won't issue for is refused, and not kept.
	wantBody(t, api("POST", "/api/v1/pails/blog/hosts", `{"host":"blog.not-mine.example"}`), http.StatusUnprocessableEntity, "PAIL_ACME_DNS_TOKEN can edit")
	wantBody(t, api("GET", "/api/v1/pails/blog", ""), 200, `"hosts":["blog.home.example"]`)
	wantBody(t, f.site("blog.not-mine.example", "/"), 404, "Nothing is hosted")

	// Certificates go when the hostname does, and when its pail does.
	api("DELETE", "/api/v1/pails/blog/hosts/blog.home.example", "")
	if fake.have["blog.home.example"] {
		t.Error("removing the hostname kept its certificate")
	}
	api("POST", "/api/v1/pails/blog/hosts", `{"host":"blog.home.example"}`)
	api("DELETE", "/api/v1/pails/blog", "")
	if len(fake.have) != 0 {
		t.Errorf("removing the pail kept certificates: %v", fake.have)
	}
}

// forge is a small Forgejo: one account, repos held as files in memory, and
// a record of the webhooks it was asked to add.
type forge struct {
	t     *testing.T
	repos map[string]map[string]string // repo → path → contents
	hooks map[string]map[string]any    // repo → what Pail asked for
	gone  []string                     // hooks Pail removed
}

func (f *forge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token forge-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	if p == "/user" {
		reply(map[string]string{"login": "chris"})
		return
	}
	if p == "/user/repos" {
		var list []map[string]any
		for name := range f.repos {
			list = append(list, map[string]any{"full_name": name, "default_branch": "main", "private": true})
		}
		reply(list)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(p, "/repos/"), "/", 4)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	repo, files := parts[0]+"/"+parts[1], f.repos[parts[0]+"/"+parts[1]]
	if files == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case parts[2] == "raw" && len(parts) == 4:
		body, ok := files[parts[3]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	case parts[2] == "archive":
		// Like the real thing, the archive wraps everything in one folder.
		wrapped := map[string]string{}
		for name, body := range files {
			wrapped[parts[1]+"/"+name] = body
		}
		w.Write(tarGz(f.t, wrapped))
	case parts[2] == "hooks" && r.Method == "POST":
		if strings.HasPrefix(repo, "locked/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var asked map[string]any
		json.NewDecoder(r.Body).Decode(&asked)
		f.hooks[repo] = asked
		reply(map[string]int{"id": 41})
	case parts[2] == "hooks" && r.Method == "DELETE":
		f.gone = append(f.gone, repo+"#"+parts[3])
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func TestPailsFromAGitHost(t *testing.T) {
	store := storage.NewMemory()
	f := newFixture(t, store)
	host := &forge{t: t, hooks: map[string]map[string]any{}, repos: map[string]map[string]string{
		"homelab/recipes":        {"index.html": "recipes v1", "about.html": "about"},
		"homelab/garden-journal": {"index.html": "x", "package.json": `{"scripts": {"build": "vite build"}, "devDependencies": {"vite": "^5"}}`},
		"locked/notes":           {"index.html": "notes"},
	}}
	ts := httptest.NewServer(host)
	defer ts.Close()
	put := func(target, body string) *httptest.ResponseRecorder { return f.api("PUT", target, []byte(body)) }
	post := func(target, body string) *httptest.ResponseRecorder { return f.api("POST", target, []byte(body)) }

	// Five hosts, none connected; the three you can run yourself say so.
	list := f.api("GET", "/api/v1/git", nil).Body.String()
	for _, want := range []string{`"kind":"github"`, `"kind":"forgejo"`, `"label":"Bitbucket"`, `"default_server":"https://gitlab.com"`, `"oauth":false`} {
		if !strings.Contains(list, want) {
			t.Errorf("git hosts lack %s: %s", want, list)
		}
	}
	if strings.Contains(list, `"connected":true`) {
		t.Errorf("something is connected before anything was: %s", list)
	}

	wantBody(t, f.api("GET", "/api/v1/git/forgejo/repos", nil), http.StatusConflict, "Forgejo isn’t connected")
	wantBody(t, put("/api/v1/git/forgejo", `{"token": "forge-token"}`), http.StatusBadRequest, "needs its server’s address")
	wantBody(t, put("/api/v1/git/forgejo", `{"server": "`+ts.URL+`", "token": "nope"}`), http.StatusUnprocessableEntity, "didn’t accept that token")
	wantBody(t, put("/api/v1/git/sourcehut", `{"token": "x"}`), http.StatusNotFound, "doesn’t know a git host")
	connected := put("/api/v1/git/forgejo", `{"server": "`+ts.URL+`", "token": "forge-token"}`)
	wantBody(t, connected, 200, `"account":"chris"`)
	if strings.Contains(connected.Body.String(), "forge-token") {
		t.Error("the API handed the token back")
	}

	wantBody(t, f.api("GET", "/api/v1/git/forgejo/repos", nil), 200, `"full":"homelab/recipes"`)
	wantBody(t, f.api("GET", "/api/v1/git/forgejo/detect?repo=homelab/recipes&branch=main", nil), 200, `"deployable":true`)
	wantBody(t, f.api("GET", "/api/v1/git/forgejo/detect?repo=homelab/garden-journal&branch=main", nil), 200, "Vite app · needs a build")

	// A repo becomes a pail: deployed as it stands, with a webhook for pushes.
	made := post("/api/v1/pails/recipes/repo", `{"host": "forgejo", "repo": "homelab/recipes", "branch": "main"}`)
	wantBody(t, made, http.StatusAccepted, `"hook":true`)
	f.svc.Wait()
	wantBody(t, f.site("recipes.pail.lan", "/"), 200, "recipes v1")
	wantBody(t, f.site("recipes.pail.lan", "/about"), 200, "about")
	p := f.api("GET", "/api/v1/pails/recipes", nil).Body.String()
	for _, want := range []string{`"source":"forgejo"`, `"repo":"homelab/recipes"`, `"revision":"main"`, `"label":"first deploy from homelab/recipes@main"`} {
		if !strings.Contains(p, want) {
			t.Errorf("pail lacks %s: %s", want, p)
		}
	}
	if strings.Contains(p, "hook_secret") {
		t.Errorf("the pail's JSON carries its hook secret: %s", p)
	}
	hook := host.hooks["homelab/recipes"]
	hookConfig, _ := hook["config"].(map[string]any)
	if hookConfig["url"] != "http://pail.lan/api/v1/hooks/recipes" || hookConfig["secret"] == "" {
		t.Fatalf("webhook Pail asked for: %v", hook)
	}
	secret := hookConfig["secret"].(string)

	// The host calls back on a push. No Pail token: the signature is the proof.
	deliver := func(body, signWith string) *httptest.ResponseRecorder {
		mac := hmac.New(sha256.New, []byte(signWith))
		mac.Write([]byte(body))
		return f.do("POST", "pail.lan", "/api/v1/hooks/recipes", []byte(body), "X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	host.repos["homelab/recipes"]["index.html"] = "recipes v2"
	wantBody(t, deliver(`{"ref": "refs/heads/main"}`, "not the secret"), http.StatusUnauthorized, "isn’t signed")
	wantBody(t, deliver(`{"ref": "refs/heads/drafts"}`, secret), 200, `"deployed":false`)
	wantBody(t, f.site("recipes.pail.lan", "/"), 200, "recipes v1")
	wantBody(t, deliver(`{"ref": "refs/heads/main"}`, secret), http.StatusAccepted, `"deployed":true`)
	f.svc.Wait()
	wantBody(t, f.site("recipes.pail.lan", "/"), 200, "recipes v2")
	wantBody(t, f.api("GET", "/api/v1/pails/recipes/deploys", nil), 200, `"label":"push to main"`)
	wantBody(t, f.do("POST", "pail.lan", "/api/v1/hooks/nope", []byte(`{}`)), http.StatusNotFound, "No pail here takes webhooks")

	// Redeploy pulls the branch again.
	host.repos["homelab/recipes"]["index.html"] = "recipes v3"
	wantBody(t, post("/api/v1/pails/recipes/redeploy", ""), http.StatusAccepted, `"label":"redeploy of main"`)
	f.svc.Wait()
	wantBody(t, f.site("recipes.pail.lan", "/"), 200, "recipes v3")

	// What Pail won't take.
	wantBody(t, post("/api/v1/pails/garden/repo", `{"host": "forgejo", "repo": "homelab/garden-journal", "branch": "main"}`), http.StatusConflict, "needs a build")
	wantBody(t, post("/api/v1/pails/recipes/repo", `{"host": "forgejo", "repo": "homelab/recipes", "branch": "main"}`), http.StatusConflict, "already a pail")
	// A repo that isn't there has nothing in it to serve, and is declined the same way.
	wantBody(t, post("/api/v1/pails/x/repo", `{"host": "forgejo", "repo": "homelab/nope", "branch": "main"}`), http.StatusConflict, "No index.html at the top")
	wantBody(t, post("/api/v1/pails/x/repo", `{"host": "github", "repo": "a/b", "branch": "main"}`), http.StatusConflict, "GitHub isn’t connected")
	if _, err := f.svc.Get("garden"); err == nil {
		t.Error("a repo Pail can't deploy left a pail behind")
	}

	// A token that can't add webhooks still makes the pail, and says so.
	locked := post("/api/v1/pails/notes/repo", `{"host": "forgejo", "repo": "locked/notes", "branch": "main"}`)
	wantBody(t, locked, http.StatusAccepted, `"hook":false`)
	wantBody(t, locked, http.StatusAccepted, "pushes won’t deploy by themselves")
	f.svc.Wait()
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "notes")

	// Connections and the repo a pail came from survive a restart.
	conns, err := githost.LoadConnections(context.Background(), store, githost.DefaultClient())
	if err != nil {
		t.Fatal(err)
	}
	f = newFixtureWith(t, store, conns)
	wantBody(t, f.api("GET", "/api/v1/git", nil), 200, `"connected":true`)
	wantBody(t, f.api("GET", "/api/v1/pails/recipes", nil), 200, `"repo":"homelab/recipes"`)

	// Removing the pail takes its webhook off the repo.
	f.api("DELETE", "/api/v1/pails/recipes", nil)
	if len(host.gone) != 1 || host.gone[0] != "homelab/recipes#41" {
		t.Errorf("webhooks removed: %v", host.gone)
	}

	// Disconnecting keeps pails, but they can't pull any more.
	if rec := f.api("DELETE", "/api/v1/git/forgejo", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
	wantBody(t, f.site("notes.pail.lan", "/"), 200, "notes")
	wantBody(t, f.api("POST", "/api/v1/pails/notes/redeploy", nil), http.StatusConflict, "Forgejo isn’t connected")
}

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/storage"
)

const token = "test-token"

type fixture struct {
	t     *testing.T
	store *storage.Memory
	svc   *pails.Service
	srv   *Server
}

func newFixture(t *testing.T, store *storage.Memory) *fixture {
	t.Helper()
	cfg := config.Config{Token: token, BaseDomain: "pail.lan", MaxUploadSize: 1 << 20, MaxDeploys: 3, MaxFunctionMemory: 1 << 30}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := pails.New(pails.Options{Store: store, BaseDomain: cfg.BaseDomain, MaxDeploys: cfg.MaxDeploys, MaxUnpackedSize: 10 << 20, Logger: logger})
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: store, svc: svc, srv: New(cfg, svc, logger, "test")}
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

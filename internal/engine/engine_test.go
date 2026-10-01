package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

func TestQualifySpellsOutDockerHub(t *testing.T) {
	for ref, want := range map[string]string{
		"python:3.13-slim":            "docker.io/library/python:3.13-slim",
		"owner/app":                   "docker.io/owner/app:latest",
		"ghcr.io/owner/app:1":         "ghcr.io/owner/app:1",
		"python@sha256:" + zeros:      "docker.io/library/python@sha256:" + zeros,
		"pail.local/pail/fn:abc":      "pail.local/pail/fn:abc",
		"docker.io/library/node:22":   "docker.io/library/node:22",
		"quay.io/buildah/stable:v1.0": "quay.io/buildah/stable:v1.0",
	} {
		if got := qualify(ref); got != want {
			t.Errorf("qualify(%q) = %q, want %q", ref, got, want)
		}
	}
}

const zeros = "0000000000000000000000000000000000000000000000000000000000000000"

// frame is a piece of a container's output as an engine sends it.
func frame(stream byte, text string) []byte {
	head := make([]byte, 8)
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(text)))
	return append(head, text...)
}

func TestDemuxJoinsBothStreamsIntoLines(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "one\ntw"))
	in.Write(frame(2, "o\n\x1b[32mthree\x1b[0m  \r\n"))
	in.Write(frame(1, "four"))
	var lines []string
	w := &lineWriter{line: func(l string) { lines = append(lines, l) }}
	if err := demux(&in, w); err != nil {
		t.Fatal(err)
	}
	w.flush()
	if got := strings.Join(lines, "|"); got != "one|two|three|four" {
		t.Errorf("lines = %q", got)
	}
}

func TestDemuxPassesUnframedOutputThrough(t *testing.T) {
	var out bytes.Buffer
	if err := demux(strings.NewReader("plain output, never framed\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "plain output, never framed\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestProgressFailsOnAnErrorInTheStream(t *testing.T) {
	var lines []string
	err := progress(strings.NewReader(`{"stream":"STEP 1/2: FROM x\n"}{"error":"no such image\n"}`), func(l string) { lines = append(lines, l) })
	if err == nil || err.Error() != "no such image" {
		t.Errorf("err = %v", err)
	}
	if len(lines) != 1 || lines[0] != "STEP 1/2: FROM x" {
		t.Errorf("lines = %q", lines)
	}
}

func TestArchivesRoundTripAndStayInside(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "bin"), 0o755)
	os.WriteFile(filepath.Join(src, "index.html"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(src, "bin", "run"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink("index.html", filepath.Join(src, "home"))

	var packed bytes.Buffer
	in := archive(func(w *tar.Writer) error {
		if err := tarDir(w, src, "work/src"); err != nil {
			return err
		}
		// What an archive from elsewhere might try.
		w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "../../escaped", Mode: 0o644, Size: 1})
		w.Write([]byte("x"))
		return w.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "work/out", Linkname: "../../.."})
	})
	if _, err := io.Copy(&packed, in); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := untar(&packed, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "work/src/index.html")); string(got) != "hi" {
		t.Errorf("index.html = %q", got)
	}
	if info, err := os.Stat(filepath.Join(dst, "work/src/bin/run")); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("bin/run should still be a program: %v", err)
	}
	if to, _ := os.Readlink(filepath.Join(dst, "work/src/home")); to != "index.html" {
		t.Errorf("home links to %q", to)
	}
	if _, err := os.Lstat(filepath.Join(dst, "work/out")); err == nil {
		t.Error("a link to outside the folder was kept")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escaped")); err == nil {
		t.Error("a file landed outside the folder")
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "escaped")); string(got) != "x" {
		t.Errorf("the file with .. in its name should land inside: %q", got)
	}
}

// fakeEngine answers the API as an engine would, for one container that
// builds a site.
type fakeEngine struct {
	t  *testing.T
	mu sync.Mutex
	// created is what the container was made with, and src what was copied
	// into it.
	created containerConfig
	src     map[string]string
	removed bool
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
		json.NewEncoder(w).Encode(imageInfo{ID: "sha256:abc"})
	case r.Method == "POST" && r.URL.Path == "/containers/create":
		json.NewDecoder(r.Body).Decode(&f.created)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"Id": "c1"})
	case r.Method == "PUT" && r.URL.Path == "/containers/c1/archive":
		f.src = map[string]string{}
		tr := tar.NewReader(r.Body)
		for {
			head, err := tr.Next()
			if err != nil {
				break
			}
			if head.Typeflag == tar.TypeReg {
				body, _ := io.ReadAll(tr)
				f.src[head.Name] = string(body)
			}
		}
	case r.Method == "POST" && r.URL.Path == "/containers/c1/start":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && r.URL.Path == "/containers/c1/logs":
		w.Write(frame(1, "→ npm run build\n"))
	case r.Method == "POST" && r.URL.Path == "/containers/c1/wait":
		json.NewEncoder(w).Encode(map[string]int{"StatusCode": 0})
	case r.Method == "HEAD" && r.URL.Path == "/containers/c1/archive":
		if r.URL.Query().Get("path") != "/work/src/apps/web/dist/index.html" {
			w.WriteHeader(http.StatusNotFound)
		}
	case r.Method == "GET" && r.URL.Path == "/containers/c1/archive":
		if got := r.URL.Query().Get("path"); got != "/work/src/apps/web/dist" {
			f.t.Errorf("copied out %q", got)
		}
		tw := tar.NewWriter(w)
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "dist/", Mode: 0o755})
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "dist/index.html", Mode: 0o644, Size: 5})
		tw.Write([]byte("built"))
		tw.Close()
	case r.Method == "DELETE" && r.URL.Path == "/containers/c1":
		f.removed = true
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("the engine was asked for %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// serve puts a fake engine behind a socket, and returns a Runner for it.
func serve(t *testing.T, engine http.Handler) *Runner {
	t.Helper()
	// A socket's path has to be short, shorter than a test's own folder.
	dir, err := os.MkdirTemp("", "pail")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "e.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: engine}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return New(Config{Socket: "unix://" + socket, Network: "pail", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBuildSiteRunsTheBuildInAContainerAndTakesWhatItLeaves(t *testing.T) {
	engine := &fakeEngine{t: t}
	r := serve(t, engine)

	var lines []string
	built, err := r.BuildSite(context.Background(), microvm.BuildRequest{
		Fill: func(dir string) error {
			os.MkdirAll(filepath.Join(dir, "apps/web"), 0o755)
			return os.WriteFile(filepath.Join(dir, "apps/web/package.json"), []byte("{}"), 0o644)
		},
		Dir: "apps/web",
		Log: func(l string) { lines = append(lines, l) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Cleanup()

	if built.Output != "dist" {
		t.Errorf("output = %q", built.Output)
	}
	if got, _ := os.ReadFile(filepath.Join(built.Dir, "index.html")); string(got) != "built" {
		t.Errorf("index.html = %q", got)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.created.Image != "docker.io/library/node:22-slim" || engine.created.Entrypoint[0] != "/bin/sh" {
		t.Errorf("the build ran as %+v", engine.created)
	}
	if got := strings.Join(engine.created.Env, " "); got != "PAIL_DIR=apps/web" {
		t.Errorf("env = %q", got)
	}
	if engine.created.Labels[labelInstance] != "pail" {
		t.Errorf("labels = %v", engine.created.Labels)
	}
	if engine.src["work/src/apps/web/package.json"] != "{}" {
		t.Errorf("the source went in as %v", engine.src)
	}
	if !engine.removed {
		t.Error("the build's container was left behind")
	}
	if len(lines) != 1 || lines[0] != "→ npm run build" {
		t.Errorf("log = %q", lines)
	}
}

func TestAnEngineThatSaysNoIsQuoted(t *testing.T) {
	r := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"no space left on device"}`))
	}))
	err := r.api.start(context.Background(), "c1")
	if err == nil || err.Error() != "no space left on device" {
		t.Errorf("err = %v", err)
	}
	if notFound(err) {
		t.Error("a 500 isn't a 404")
	}
}

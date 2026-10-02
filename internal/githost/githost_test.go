package githost

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chrisdmacrae/pail/internal/storage"
)

// fake stands in for a git host's API. Each route answers in the shape the
// real host does, and records what it was asked.
type fake struct {
	t      *testing.T
	auth   string // the Authorization header the host expects
	routes map[string]any
	seen   []string
	hook   map[string]any
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.RequestURI()
	f.seen = append(f.seen, key)
	if user, pass, ok := r.BasicAuth(); ok {
		if "basic "+user+":"+pass != f.auth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	} else if r.Header.Get("Authorization") != f.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == "POST" {
		json.NewDecoder(r.Body).Decode(&f.hook)
	}
	answer, ok := f.routes[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch v := answer.(type) {
	case string:
		io.WriteString(w, v)
	case int:
		w.WriteHeader(v)
	default:
		json.NewEncoder(w).Encode(v)
	}
}

type obj = map[string]any

func TestClients(t *testing.T) {
	const repo, branch = "homelab/recipes", "main"
	hookURL := "https://pail.lan/api/v1/hooks/recipes"

	cases := []struct {
		kind  Kind
		token string
		auth  string
		// routes maps each call the client should make to the host's answer.
		routes map[string]any
		// hookSays checks what the client asked the host to do with pushes.
		hookSays func(obj) bool
		hookID   string
	}{
		{
			kind: GitHub, token: "ghp_x", auth: "Bearer ghp_x", hookID: "12",
			routes: map[string]any{
				"GET /user": obj{"login": "chris"},
				"GET /user/repos?sort=pushed&per_page=100":                        []obj{{"full_name": repo, "default_branch": "main", "private": true}},
				"GET /repos/homelab/recipes/contents/package.json?ref=main":       `{"name":"recipes"}`,
				"GET /repos/homelab/recipes/tarball/main":                         "TARBALL",
				"POST /repos/homelab/recipes/hooks":                               obj{"id": 12},
				"DELETE /repos/homelab/recipes/hooks/12":                          204,
				"GET /repos/homelab/recipes/contents/pail.json?ref=main":          404,
				"GET /repos/homelab/recipes/contents/missing%20file.txt?ref=main": 404,
			},
			hookSays: func(h obj) bool {
				c, _ := h["config"].(obj)
				return h["name"] == "web" && c["url"] == hookURL && c["secret"] == "s3cret" && c["content_type"] == "json" && c["insecure_ssl"] == "1"
			},
		},
		{
			kind: GitLab, token: "glpat-x", auth: "Bearer glpat-x", hookID: "7",
			routes: map[string]any{
				"GET /api/v4/user": obj{"username": "chris"},
				"GET /api/v4/projects?membership=true&simple=true&order_by=last_activity_at&per_page=100": []obj{{"path_with_namespace": repo, "default_branch": "main", "visibility": "private"}},
				"GET /api/v4/projects/homelab%2Frecipes/repository/files/package.json/raw?ref=main":       `{"name":"recipes"}`,
				"GET /api/v4/projects/homelab%2Frecipes/repository/archive.tar.gz?sha=main":               "TARBALL",
				"POST /api/v4/projects/homelab%2Frecipes/hooks":                                           obj{"id": 7},
				"DELETE /api/v4/projects/homelab%2Frecipes/hooks/7":                                       204,
			},
			hookSays: func(h obj) bool {
				return h["url"] == hookURL && h["token"] == "s3cret" && h["push_events"] == true && h["enable_ssl_verification"] == false
			},
		},
		{
			kind: Forgejo, token: "fj-x", auth: "token fj-x", hookID: "3",
			routes: map[string]any{
				"GET /api/v1/user":                                            obj{"login": "chris"},
				"GET /api/v1/user/repos?limit=50":                             []obj{{"full_name": repo, "default_branch": "main", "private": true}},
				"GET /api/v1/repos/homelab/recipes/raw/package.json?ref=main": `{"name":"recipes"}`,
				"GET /api/v1/repos/homelab/recipes/archive/main.tar.gz":       "TARBALL",
				"POST /api/v1/repos/homelab/recipes/hooks":                    obj{"id": 3},
				"DELETE /api/v1/repos/homelab/recipes/hooks/3":                204,
			},
			hookSays: func(h obj) bool {
				c, _ := h["config"].(obj)
				return h["type"] == "gitea" && c["url"] == hookURL && c["secret"] == "s3cret"
			},
		},
		{
			kind: Bitbucket, token: "chris:app-pass", auth: "basic chris:app-pass", hookID: "{uuid-1}",
			routes: map[string]any{
				"GET /2.0/user": obj{"username": "chris"},
				"GET /2.0/repositories?role=member&sort=-updated_on&pagelen=100": obj{"values": []obj{{"full_name": repo, "is_private": true, "mainbranch": obj{"name": "main"}}}},
				"GET /2.0/repositories/homelab/recipes/src/main/package.json":    `{"name":"recipes"}`,
				"GET /homelab/recipes/get/main.tar.gz":                           "TARBALL",
				"POST /2.0/repositories/homelab/recipes/hooks":                   obj{"uuid": "{uuid-1}"},
				"DELETE /2.0/repositories/homelab/recipes/hooks/%7Buuid-1%7D":    204,
			},
			hookSays: func(h obj) bool {
				return h["url"] == hookURL && h["secret"] == "s3cret" && h["active"] == true
			},
		},
	}

	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			f := &fake{t: t, auth: c.auth, routes: c.routes}
			ts := httptest.NewServer(f)
			defer ts.Close()
			ctx := context.Background()
			client, err := New(c.kind, ts.URL, c.token, ts.Client())
			if err != nil {
				t.Fatal(err)
			}

			if who, err := client.Account(ctx); err != nil || who != "chris" {
				t.Errorf("Account: %q, %v", who, err)
			}
			repos, err := client.Repos(ctx)
			if err != nil || len(repos) != 1 || repos[0] != (Repo{Full: repo, Branch: "main", Private: true}) {
				t.Errorf("Repos: %+v, %v", repos, err)
			}
			if b, err := client.ReadFile(ctx, repo, branch, "package.json"); err != nil || string(b) != `{"name":"recipes"}` {
				t.Errorf("ReadFile: %q, %v", b, err)
			}
			if _, err := client.ReadFile(ctx, repo, branch, "missing file.txt"); err != ErrNotFound {
				t.Errorf("ReadFile of a missing file: %v", err)
			}

			var buf bytes.Buffer
			if err := client.Archive(ctx, repo, branch, &buf, 100); err != nil || buf.String() != "TARBALL" {
				t.Errorf("Archive: %q, %v", buf.String(), err)
			}
			if err := client.Archive(ctx, repo, branch, io.Discard, 3); err != ErrTooLarge {
				t.Errorf("Archive past the limit: %v", err)
			}

			id, err := client.AddHook(ctx, repo, hookURL, "s3cret", false)
			if err != nil || id != c.hookID || !c.hookSays(f.hook) {
				t.Errorf("AddHook: id %q, %v, asked %v", id, err, f.hook)
			}
			if err := client.RemoveHook(ctx, repo, id); err != nil {
				t.Errorf("RemoveHook: %v (%v)", err, f.seen[len(f.seen)-1])
			}

			// A token the host doesn't know is reported as such.
			bad, _ := New(c.kind, ts.URL, "nope", ts.Client())
			if _, err := bad.Repos(ctx); err != ErrUnauthorized {
				t.Errorf("wrong token: %v", err)
			}
		})
	}
}

// files is a Client that only has files to read.
type files map[string]string

func (f files) ReadFile(_ context.Context, _, _, path string) ([]byte, error) {
	if body, ok := f[path]; ok {
		return []byte(body), nil
	}
	return nil, ErrNotFound
}
func (files) Account(context.Context) (string, error)                               { return "", nil }
func (files) Repos(context.Context) ([]Repo, error)                                 { return nil, nil }
func (files) Archive(context.Context, string, string, io.Writer, int64) error       { return nil }
func (files) AddHook(context.Context, string, string, string, bool) (string, error) { return "", nil }
func (files) RemoveHook(context.Context, string, string) error                      { return nil }

func TestDetect(t *testing.T) {
	cases := []struct {
		name   string
		repo   files
		deploy bool
		says   string
	}{
		{"plain site", files{"index.html": "<h1>hi</h1>"}, true, "index.html at the top"},
		{"pail.json with a static folder", files{"pail.json": `{"static": "./public"}`}, true, "pail.json serves ./public"},
		{"site with a package.json but nothing to build", files{"index.html": "x", "package.json": `{"scripts": {"test": "x"}}`}, true, "index.html at the top"},
		{"vite app", files{"index.html": "x", "package.json": `{"scripts": {"build": "vite build"}, "devDependencies": {"vite": "^5"}}`}, false, "Vite app · needs a build"},
		{"astro site", files{"package.json": `{"scripts": {"build": "astro build"}, "dependencies": {"astro": "^5", "vite": "^5"}}`}, false, "Astro site · needs a build"},
		{"some node project", files{"package.json": `{"scripts": {"build": "tsc"}}`}, false, "Node project · needs a build"},
		{"functions", files{"index.html": "x", "pail.json": `{"functions": {"api": {"src": "./fn"}}}`}, false, "server code, which this Pail can’t run"},
		{"a container, with nowhere to run it", files{"Dockerfile": "FROM x", "pail.json": `{"containers": {"api": {"port": 80}}}`}, false, "server code, which this Pail can’t run"},
		{"nothing to serve", files{"README.md": "hello"}, false, "No index.html at the top"},
	}
	for _, c := range cases {
		got, err := Detect(context.Background(), c.repo, "o/r", "main", "", false)
		if err != nil || got.Deployable != c.deploy || !strings.Contains(got.Summary, c.says) {
			t.Errorf("%s: %+v, %v; want deployable=%v saying %q", c.name, got, err, c.deploy, c.says)
		}
	}

	// A Pail that can run builds takes the projects that need one.
	vite := files{"index.html": "x", "package.json": `{"scripts": {"build": "vite build"}, "devDependencies": {"vite": "^5"}}`}
	if got, err := Detect(context.Background(), vite, "o/r", "main", "", true); err != nil || !got.Deployable || !strings.Contains(got.Summary, "Vite app · Pail builds it") {
		t.Errorf("a Vite app where builds run: %+v, %v", got, err)
	}
	// A project with containers is built by its Dockerfiles, even one whose
	// package.json has a build script.
	app := files{"Dockerfile": "FROM x", "package.json": `{"scripts": {"build": "tsc"}}`, "pail.json": `{"containers": {"api": {"port": 80}}}`}
	if got, err := Detect(context.Background(), app, "o/r", "main", "", true); err != nil || !got.Deployable || !strings.Contains(got.Summary, "A container · Pail builds its Dockerfile") {
		t.Errorf("a container where microVMs run: %+v, %v", got, err)
	}
	fns := files{"fn/main.py": "x", "pail.json": `{"functions": {"api": {"src": "./fn"}}}`}
	if got, err := Detect(context.Background(), fns, "o/r", "main", "", true); err != nil || !got.Deployable || !strings.Contains(got.Summary, "A function · Pail builds it and runs it on request") {
		t.Errorf("a function where microVMs run: %+v, %v", got, err)
	}
	image := files{"pail.json": `{"containers": {"web": {"image": "nginx:1.27", "port": 80, "memory": "64MB"}}}`}
	if got, err := Detect(context.Background(), image, "o/r", "main", "", true); err != nil || !got.Deployable || !strings.Contains(got.Summary, "Pail pulls nginx:1.27 and runs it") {
		t.Errorf("a container from a registry: %+v, %v", got, err)
	}
}

// A repo with several pails is looked at one folder at a time.
func TestDetectInAFolder(t *testing.T) {
	repo := files{
		"README.md":           "two pails",
		"apps/web/index.html": "x",
		"apps/docs/pail.json": `{"static": "./public"}`,
		"apps/api/pail.json":  `{"functions": {"api": {"src": "./fn"}}}`,
		"apps/blog/pail.json": `{}`,
	}
	cases := []struct {
		dir    string
		deploy bool
		says   string
	}{
		{"", false, "No index.html at the top, and no pail.json"},
		{"apps/web", true, "index.html in apps/web"},
		{"apps/docs", true, "pail.json serves ./public"},
		{"apps/api", true, "A function · Pail builds it"},
		{"apps/blog", true, "pail.json serves apps/blog"},
		{"apps/nope", false, "No index.html in apps/nope, and no pail.json"},
	}
	for _, c := range cases {
		got, err := Detect(context.Background(), repo, "o/r", "main", c.dir, true)
		if err != nil || got.Deployable != c.deploy || !strings.Contains(got.Summary, c.says) {
			t.Errorf("%q: %+v, %v; want deployable=%v saying %q", c.dir, got, err, c.deploy, c.says)
		}
	}
}

func TestReadPush(t *testing.T) {
	const secret = "s3cret"
	sign := func(body string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		return hex.EncodeToString(mac.Sum(nil))
	}
	push := `{"ref": "refs/heads/main", "after": "4bf74c3"}`
	tag := `{"ref": "refs/tags/v1", "after": "4bf74c3"}`
	gone := `{"ref": "refs/heads/main", "after": "0000000000000000000000000000000000000000"}`
	bb := `{"push": {"changes": [{"new": {"type": "branch", "name": "main", "target": {"hash": "4bf74c3"}}}]}}`

	cases := []struct {
		name          string
		kind          Kind
		header, value string
		body          string
		genuine       bool
		branch        string
		commit        string
	}{
		{"github push", GitHub, "X-Hub-Signature-256", "sha256=" + sign(push), push, true, "main", "4bf74c3"},
		{"github tag", GitHub, "X-Hub-Signature-256", "sha256=" + sign(tag), tag, true, "", ""},
		{"github ping", GitHub, "X-Hub-Signature-256", "sha256=" + sign(`{"zen": "x"}`), `{"zen": "x"}`, true, "", ""},
		{"github forged", GitHub, "X-Hub-Signature-256", "sha256=" + sign("other"), push, false, "", ""},
		{"github unsigned", GitHub, "X-Other", "x", push, false, "", ""},
		// A branch that was deleted is at no commit.
		{"github branch deleted", GitHub, "X-Hub-Signature-256", "sha256=" + sign(gone), gone, true, "main", ""},
		{"gitlab push", GitLab, "X-Gitlab-Token", secret, push, true, "main", "4bf74c3"},
		{"gitlab wrong token", GitLab, "X-Gitlab-Token", "nope", push, false, "", ""},
		{"gitea push", Gitea, "X-Gitea-Signature", sign(push), push, true, "main", "4bf74c3"},
		{"forgejo push", Forgejo, "X-Hub-Signature-256", "sha256=" + sign(push), push, true, "main", "4bf74c3"},
		{"bitbucket push", Bitbucket, "X-Hub-Signature", "sha256=" + sign(bb), bb, true, "main", "4bf74c3"},
		{"bitbucket forged", Bitbucket, "X-Hub-Signature", "sha256=" + sign(push), bb, false, "", ""},
	}
	for _, c := range cases {
		h := http.Header{}
		h.Set(c.header, c.value)
		got := ReadPush(c.kind, secret, h, []byte(c.body))
		if got.Genuine != c.genuine || got.Branch != c.branch || got.Commit != c.commit {
			t.Errorf("%s: %+v, want genuine=%v branch=%q commit=%q", c.name, got, c.genuine, c.branch, c.commit)
		}
	}
}

// What a push changed is known only when the delivery lists all of it.
func TestWhatAPushChanged(t *testing.T) {
	const secret = "s3cret"
	read := func(body string) Push {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		h := http.Header{}
		h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return ReadPush(GitHub, secret, h, []byte(body))
	}
	commit := `{"added": ["apps/web/new.html"], "modified": ["README.md"], "removed": ["apps/docs/old.md"]}`
	many := strings.TrimSuffix(strings.Repeat(commit+",", 20), ",")

	cases := []struct {
		name string
		body string
		want []string
	}{
		{"one commit", `{"ref": "refs/heads/main", "commits": [` + commit + `]}`, []string{"apps/web/new.html", "README.md", "apps/docs/old.md"}},
		{"a commit that changed no files", `{"ref": "refs/heads/main", "commits": [{"added": [], "modified": [], "removed": []}]}`, []string{}},
		{"as many as were pushed", `{"ref": "refs/heads/main", "total_commits": 1, "commits": [` + commit + `]}`, []string{"apps/web/new.html", "README.md", "apps/docs/old.md"}},
		{"no commits listed", `{"ref": "refs/heads/main"}`, nil},
		{"a host that lists no files", `{"ref": "refs/heads/main", "commits": [{"id": "abc"}]}`, nil},
		{"a forced push", `{"ref": "refs/heads/main", "forced": true, "commits": [` + commit + `]}`, nil},
		{"a new branch", `{"ref": "refs/heads/main", "created": true, "commits": [` + commit + `]}`, nil},
		{"fewer listed than pushed, on gitea", `{"ref": "refs/heads/main", "total_commits": 31, "commits": [` + commit + `]}`, nil},
		{"fewer listed than pushed, on gitlab", `{"ref": "refs/heads/main", "total_commits_count": 31, "commits": [` + commit + `]}`, nil},
		{"a long push", `{"ref": "refs/heads/main", "commits": [` + many + `]}`, nil},
	}
	for _, c := range cases {
		got := read(c.body)
		if !got.Genuine || got.Branch != "main" || !slices.Equal(got.Changed, c.want) || (got.Changed == nil) != (c.want == nil) {
			t.Errorf("%s: %+v, want changed=%v", c.name, got, c.want)
		}
	}

	// Which pails a push is for.
	touches := []struct {
		changed []string
		dir     string
		watch   []string
		want    bool
	}{
		{[]string{"apps/web/index.html"}, "apps/web", nil, true},
		{[]string{"apps/docs/index.html", "README.md"}, "apps/web", nil, false},
		{[]string{"apps/website/index.html"}, "apps/web", nil, false}, // a folder that only starts the same
		{[]string{"README.md"}, "", nil, true},                        // a pail that is the whole repo
		{nil, "apps/web", nil, true},                                  // a push that doesn't say
		{[]string{}, "apps/web", nil, false},
		// What pail.json watches, a folder or a file.
		{[]string{"packages/ui/button.js"}, "apps/web", []string{"packages/ui"}, true},
		{[]string{"packages/api/x.js"}, "apps/web", []string{"packages/ui", "shared.css"}, false},
		{[]string{"shared.css"}, "apps/web", []string{"packages/ui", "shared.css"}, true},
		// A workspace's own files, in the folders above the pail.
		{[]string{"pnpm-lock.yaml"}, "apps/web", nil, true},
		{[]string{"apps/package.json"}, "apps/web", nil, true},
		{[]string{"packages/ui/package.json"}, "apps/web", nil, false},
		{[]string{".github/package.json"}, "apps/web", nil, false},
	}
	for _, c := range touches {
		if got := (Push{Changed: c.changed}).Touches(c.dir, c.watch); got != c.want {
			t.Errorf("a push of %v, for %q watching %v: %v, want %v", c.changed, c.dir, c.watch, got, c.want)
		}
	}
}

func TestConnections(t *testing.T) {
	f := &fake{t: t, auth: "token fj-x", routes: map[string]any{"GET /api/v1/user": obj{"login": "chris"}}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	ctx := context.Background()
	store := storage.NewMemory()
	conns, err := LoadConnections(ctx, store, ts.Client())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conns.Client(ctx, "recipes", Forgejo); err != ErrNotConnected {
		t.Errorf("before connecting: %v", err)
	}
	if _, _, err := conns.Connect(ctx, Forgejo, "", "fj-x"); err == nil || !strings.Contains(err.Error(), "server") {
		t.Errorf("Forgejo with no server: %v", err)
	}
	if _, _, err := conns.Connect(ctx, Forgejo, ts.URL+"/", "wrong"); err != ErrUnauthorized {
		t.Errorf("a token the host rejects: %v", err)
	}
	id, conn, err := conns.Connect(ctx, Forgejo, ts.URL+"/", " fj-x\n")
	if err != nil || id == "" || conn.Account != "chris" || conn.Server != ts.URL {
		t.Fatalf("Connect: %q, %+v, %v", id, conn, err)
	}

	// The connection waits for its pail: no pail has it, and nothing is stored.
	if got, ok := conns.Waiting(Forgejo, id); !ok || got != conn {
		t.Errorf("the waiting connection: %+v, %v", got, ok)
	}
	if _, ok := conns.Waiting(GitHub, id); ok {
		t.Error("a Forgejo connection answered as a GitHub one")
	}
	if _, err := conns.WaitingClient(ctx, Forgejo, id); err != nil {
		t.Errorf("a client for the waiting connection: %v", err)
	}
	if _, err := conns.WaitingClient(ctx, Forgejo, "made-up"); err != ErrNotConnected {
		t.Errorf("a client for an id nobody was given: %v", err)
	}
	if _, err := conns.Client(ctx, "recipes", Forgejo); err != ErrNotConnected {
		t.Errorf("a pail before it is given the connection: %v", err)
	}
	if keys, _ := store.List(ctx, "git/"); len(keys) != 0 {
		t.Errorf("a waiting connection was stored: %v", keys)
	}

	// Given to a pail, it is that pail's alone, and waits for no other.
	if err := conns.Give(ctx, id, "recipes"); err != nil {
		t.Fatal(err)
	}
	if _, err := conns.Client(ctx, "recipes", Forgejo); err != nil {
		t.Errorf("the pail it was given to: %v", err)
	}
	if _, err := conns.Client(ctx, "garden", Forgejo); err != ErrNotConnected {
		t.Errorf("another pail: %v", err)
	}
	if _, ok := conns.Waiting(Forgejo, id); ok {
		t.Error("a connection given to a pail still waits for one")
	}
	if err := conns.Give(ctx, id, "garden"); err != ErrNotConnected {
		t.Errorf("giving it a second time: %v", err)
	}

	// A connection nobody makes a pail with is forgotten.
	dropped, _, _ := conns.Connect(ctx, Forgejo, ts.URL, "fj-x")
	conns.Drop(dropped)
	late, _, _ := conns.Connect(ctx, Forgejo, ts.URL, "fj-x")
	conns.waiting[late] = waiting{conn: conn, since: time.Now().Add(-2 * waitWindow)}
	for _, gone := range []string{dropped, late} {
		if _, ok := conns.Waiting(Forgejo, gone); ok {
			t.Errorf("connection %s still waits", gone)
		}
	}

	// A pail's connection is kept: a restart still has it.
	again, err := LoadConnections(ctx, store, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := again.get(slot{pail: "recipes"}); !ok || got != conn {
		t.Errorf("after a restart: %+v", got)
	}
	if err := again.Forget(ctx, "recipes"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := store.List(ctx, "git/"); len(keys) != 0 {
		t.Errorf("forgetting left %v", keys)
	}

	// A pail made when every pail of a host shared one connection has none
	// of its own, and still pulls with that one.
	b, _ := json.Marshal(conn)
	store.Put(ctx, "git/forgejo.json", bytes.NewReader(b), int64(len(b)), "application/json")
	old, err := LoadConnections(ctx, store, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Client(ctx, "from-before", Forgejo); err != nil {
		t.Errorf("a pail from before: %v", err)
	}
	if _, err := old.Client(ctx, "from-before", GitHub); err != ErrNotConnected {
		t.Errorf("a pail from before, on another host: %v", err)
	}
	if _, ok := old.Waiting(Forgejo, ""); ok {
		t.Error("the shared connection is offered to new pails")
	}
}

// oauthHost is a git host's sign-in: it hands out a token for a code, and
// the next token for a refresh token, which works once.
type oauthHost struct {
	t        *testing.T
	basic    bool // expects the app's credentials as basic auth
	issued   int
	lastForm map[string]string
}

func (o *oauthHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/user") {
		// Only the newest token is good.
		if !strings.HasSuffix(r.Header.Get("Authorization"), fmt.Sprintf("access-%d", o.issued)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(obj{"login": "chris", "username": "chris"})
		return
	}
	r.ParseForm()
	o.lastForm = map[string]string{}
	for k := range r.PostForm {
		o.lastForm[k] = r.PostForm.Get(k)
	}
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if o.basic {
		id, secret, _ = r.BasicAuth()
	}
	good := id == "app-id" && secret == "app-secret" &&
		((r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") == "the-code") ||
			(r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == fmt.Sprintf("refresh-%d", o.issued)))
	if !good || r.Header.Get("Accept") != "application/json" {
		json.NewEncoder(w).Encode(obj{"error": "bad_verification_code", "error_description": "The code is incorrect or expired."})
		return
	}
	o.issued++
	json.NewEncoder(w).Encode(obj{
		"access_token": fmt.Sprintf("access-%d", o.issued), "refresh_token": fmt.Sprintf("refresh-%d", o.issued), "expires_in": 30,
	})
}

func TestOAuth(t *testing.T) {
	ctx := context.Background()
	redirect := "https://pail.lan/oauth/callback/forgejo"

	// Where each host is sent to sign in, and what it's asked for.
	for kind, want := range map[Kind]string{
		GitHub:    "https://github.com/login/oauth/authorize?client_id=app-id&redirect_uri=https%3A%2F%2Fpail.lan%2Fcb&response_type=code&scope=repo&state=xyz",
		GitLab:    "https://gitlab.com/oauth/authorize?client_id=app-id&redirect_uri=https%3A%2F%2Fpail.lan%2Fcb&response_type=code&scope=api&state=xyz",
		Bitbucket: "https://bitbucket.org/site/oauth2/authorize?client_id=app-id&redirect_uri=https%3A%2F%2Fpail.lan%2Fcb&response_type=code&state=xyz",
	} {
		if got := AuthorizeURL(kind, App{ClientID: "app-id"}, "https://pail.lan/cb", "xyz"); got != want {
			t.Errorf("%s sign-in address:\n got %s\nwant %s", kind, got, want)
		}
	}
	if got := AuthorizeURL(Forgejo, App{ClientID: "app-id", Server: "https://git.home.example/"}, "https://pail.lan/cb", "xyz"); !strings.HasPrefix(got, "https://git.home.example/login/oauth/authorize?client_id=app-id&") {
		t.Errorf("Forgejo sign-in address: %s", got)
	}

	for _, kind := range []Kind{Forgejo, Bitbucket} {
		t.Run(string(kind), func(t *testing.T) {
			host := &oauthHost{t: t, basic: kind == Bitbucket}
			ts := httptest.NewServer(host)
			defer ts.Close()
			store := storage.NewMemory()
			conns, _ := LoadConnections(ctx, store, ts.Client())
			conns.BaseURLs = map[Kind]string{Bitbucket: ts.URL}

			if _, _, err := conns.ConnectOAuth(ctx, kind, "the-code", redirect); err != ErrNoOAuth {
				t.Fatalf("with no app set up: %v", err)
			}
			conns.Apps = map[Kind]App{kind: {ClientID: "app-id", ClientSecret: "app-secret", Server: ts.URL}}

			if id, _, err := conns.ConnectOAuth(ctx, kind, "a-stale-code", redirect); err == nil || id != "" || !strings.Contains(err.Error(), "incorrect or expired") {
				t.Errorf("a bad code: %q, %v", id, err)
			}

			id, conn, err := conns.ConnectOAuth(ctx, kind, "the-code", redirect)
			if err != nil || !conn.OAuth || conn.Token != "access-1" || conn.Refresh != "refresh-1" || conn.Account != "chris" || conn.Expires.IsZero() {
				t.Fatalf("ConnectOAuth: %+v, %v", conn, err)
			}
			if host.lastForm["redirect_uri"] != redirect {
				t.Errorf("the code was redeemed with redirect_uri %q", host.lastForm["redirect_uri"])
			}

			// The token lasts 30 seconds here, so it is already due: the next
			// use renews it, while it waits for its pail as much as after.
			waiting, err := conns.WaitingClient(ctx, kind, id)
			if err != nil {
				t.Fatal(err)
			}
			if who, err := waiting.Account(ctx); err != nil || who != "chris" || host.issued != 2 {
				t.Errorf("renewing a waiting sign-in: %q, %v, %d tokens issued", who, err, host.issued)
			}
			if err := conns.Give(ctx, id, "recipes"); err != nil {
				t.Fatal(err)
			}
			client, err := conns.Client(ctx, "recipes", kind)
			if err != nil {
				t.Fatal(err)
			}
			if who, err := client.Account(ctx); err != nil || who != "chris" {
				t.Errorf("after renewing: %q, %v", who, err)
			}
			if host.issued != 3 {
				t.Errorf("tokens issued: %d, want the first and a renewal each time", host.issued)
			}
			again, _ := LoadConnections(ctx, store, ts.Client())
			if got, _ := again.get(slot{pail: "recipes"}); got.Token != "access-3" || got.Refresh != "refresh-3" {
				t.Errorf("the renewed token wasn't kept: %+v", got)
			}

			// A sign-in that can't be renewed says so.
			host.issued = 9
			if _, err := conns.Client(ctx, "recipes", kind); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "has run out") {
				t.Errorf("a refresh token the host no longer takes: %v", err)
			}
		})
	}
}

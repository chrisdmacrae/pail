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
	"strings"
	"testing"

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
		{"functions", files{"index.html": "x", "pail.json": `{"functions": {"api": {"src": "./fn"}}}`}, false, "functions or containers"},
		{"nothing to serve", files{"README.md": "hello"}, false, "No index.html at the top"},
	}
	for _, c := range cases {
		got, err := Detect(context.Background(), c.repo, "o/r", "main", false)
		if err != nil || got.Deployable != c.deploy || !strings.Contains(got.Summary, c.says) {
			t.Errorf("%s: %+v, %v; want deployable=%v saying %q", c.name, got, err, c.deploy, c.says)
		}
	}

	// A Pail that can run builds takes the projects that need one.
	vite := files{"index.html": "x", "package.json": `{"scripts": {"build": "vite build"}, "devDependencies": {"vite": "^5"}}`}
	if got, err := Detect(context.Background(), vite, "o/r", "main", true); err != nil || !got.Deployable || !strings.Contains(got.Summary, "Vite app · Pail builds it") {
		t.Errorf("a Vite app where builds run: %+v, %v", got, err)
	}
}

func TestReadPush(t *testing.T) {
	const secret = "s3cret"
	sign := func(body string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		return hex.EncodeToString(mac.Sum(nil))
	}
	push := `{"ref": "refs/heads/main"}`
	tag := `{"ref": "refs/tags/v1"}`
	bb := `{"push": {"changes": [{"new": {"type": "branch", "name": "main"}}]}}`

	cases := []struct {
		name          string
		kind          Kind
		header, value string
		body          string
		genuine       bool
		branch        string
	}{
		{"github push", GitHub, "X-Hub-Signature-256", "sha256=" + sign(push), push, true, "main"},
		{"github tag", GitHub, "X-Hub-Signature-256", "sha256=" + sign(tag), tag, true, ""},
		{"github ping", GitHub, "X-Hub-Signature-256", "sha256=" + sign(`{"zen": "x"}`), `{"zen": "x"}`, true, ""},
		{"github forged", GitHub, "X-Hub-Signature-256", "sha256=" + sign("other"), push, false, ""},
		{"github unsigned", GitHub, "X-Other", "x", push, false, ""},
		{"gitlab push", GitLab, "X-Gitlab-Token", secret, push, true, "main"},
		{"gitlab wrong token", GitLab, "X-Gitlab-Token", "nope", push, false, ""},
		{"gitea push", Gitea, "X-Gitea-Signature", sign(push), push, true, "main"},
		{"forgejo push", Forgejo, "X-Hub-Signature-256", "sha256=" + sign(push), push, true, "main"},
		{"bitbucket push", Bitbucket, "X-Hub-Signature", "sha256=" + sign(bb), bb, true, "main"},
		{"bitbucket forged", Bitbucket, "X-Hub-Signature", "sha256=" + sign(push), bb, false, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		h.Set(c.header, c.value)
		got := ReadPush(c.kind, secret, h, []byte(c.body))
		if got.Genuine != c.genuine || got.Branch != c.branch {
			t.Errorf("%s: %+v, want genuine=%v branch=%q", c.name, got, c.genuine, c.branch)
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

	if _, err := conns.Client(ctx, Forgejo); err != ErrNotConnected {
		t.Errorf("before connecting: %v", err)
	}
	if _, err := conns.Connect(ctx, Forgejo, "", "fj-x"); err == nil || !strings.Contains(err.Error(), "server") {
		t.Errorf("Forgejo with no server: %v", err)
	}
	if _, err := conns.Connect(ctx, Forgejo, ts.URL+"/", "wrong"); err != ErrUnauthorized {
		t.Errorf("a token the host rejects: %v", err)
	}
	if _, ok := conns.Get(Forgejo); ok {
		t.Error("a rejected token was kept")
	}
	conn, err := conns.Connect(ctx, Forgejo, ts.URL+"/", " fj-x\n")
	if err != nil || conn.Account != "chris" || conn.Server != ts.URL {
		t.Fatalf("Connect: %+v, %v", conn, err)
	}

	// Connections are kept: a restart still has them.
	again, err := LoadConnections(ctx, store, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := again.Get(Forgejo); !ok || got != conn {
		t.Errorf("after a restart: %+v", got)
	}
	if err := again.Disconnect(ctx, Forgejo); err != nil {
		t.Fatal(err)
	}
	if keys, _ := store.List(ctx, "git/"); len(keys) != 0 {
		t.Errorf("disconnecting left %v", keys)
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

			if _, err := conns.ConnectOAuth(ctx, kind, "the-code", redirect); err != ErrNoOAuth {
				t.Fatalf("with no app set up: %v", err)
			}
			conns.Apps = map[Kind]App{kind: {ClientID: "app-id", ClientSecret: "app-secret", Server: ts.URL}}

			if _, err := conns.ConnectOAuth(ctx, kind, "a-stale-code", redirect); err == nil || !strings.Contains(err.Error(), "incorrect or expired") {
				t.Errorf("a bad code: %v", err)
			}
			if _, ok := conns.Get(kind); ok {
				t.Fatal("a failed sign-in left a connection")
			}

			conn, err := conns.ConnectOAuth(ctx, kind, "the-code", redirect)
			if err != nil || !conn.OAuth || conn.Token != "access-1" || conn.Refresh != "refresh-1" || conn.Account != "chris" || conn.Expires.IsZero() {
				t.Fatalf("ConnectOAuth: %+v, %v", conn, err)
			}
			if host.lastForm["redirect_uri"] != redirect {
				t.Errorf("the code was redeemed with redirect_uri %q", host.lastForm["redirect_uri"])
			}

			// The token lasts 30 seconds here, so it is already due: the next
			// use renews it, and the renewal is kept.
			client, err := conns.Client(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			if who, err := client.Account(ctx); err != nil || who != "chris" {
				t.Errorf("after renewing: %q, %v", who, err)
			}
			if host.issued != 2 {
				t.Errorf("tokens issued: %d, want the first and one renewal", host.issued)
			}
			again, _ := LoadConnections(ctx, store, ts.Client())
			if got, _ := again.Get(kind); got.Token != "access-2" || got.Refresh != "refresh-2" {
				t.Errorf("the renewed token wasn't kept: %+v", got)
			}

			// A sign-in that can't be renewed says to sign in again.
			host.issued = 9
			if _, err := conns.Client(ctx, kind); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "Sign in again") {
				t.Errorf("a refresh token the host no longer takes: %v", err)
			}
		})
	}
}

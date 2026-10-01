// Package githost talks to the git hosts a pail can come from: GitHub,
// GitLab, Bitbucket, Gitea and Forgejo. Each is reached with an access token,
// and each answers the same few questions: who is this, what repos are
// there, what's in one, and tell me when it's pushed to.
package githost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Kind string

const (
	GitHub    Kind = "github"
	GitLab    Kind = "gitlab"
	Bitbucket Kind = "bitbucket"
	Gitea     Kind = "gitea"
	Forgejo   Kind = "forgejo"
)

// Kinds lists the hosts in the order the UI shows them.
var Kinds = []Kind{GitHub, GitLab, Bitbucket, Gitea, Forgejo}

var labels = map[Kind]string{GitHub: "GitHub", GitLab: "GitLab", Bitbucket: "Bitbucket", Gitea: "Gitea", Forgejo: "Forgejo"}

// Label is the host's name as people write it.
func (k Kind) Label() string { return labels[k] }

func (k Kind) Valid() bool { return labels[k] != "" }

// SelfHostable reports whether the host can run on a server of your own, so
// connecting it needs to know which.
func (k Kind) SelfHostable() bool { return k == GitLab || k == Gitea || k == Forgejo }

// DefaultServer is where the host lives when you don't run your own. Gitea
// and Forgejo have no such place.
func (k Kind) DefaultServer() string {
	switch k {
	case GitHub:
		return "https://api.github.com"
	case GitLab:
		return "https://gitlab.com"
	case Bitbucket:
		return "https://api.bitbucket.org"
	}
	return ""
}

var (
	ErrUnauthorized = errors.New("the git host rejected the token")
	ErrNotFound     = errors.New("not found on the git host")
	ErrTooLarge     = errors.New("the repository is larger than this Pail takes")
)

// Repo is a repository the token can see.
type Repo struct {
	// Full is its name with its owner: "homelab/garden-journal".
	Full string `json:"full"`
	// Branch is its default branch.
	Branch  string `json:"branch"`
	Private bool   `json:"private"`
}

// Client is one connection to one git host.
type Client interface {
	// Account says whose token this is. It is also how a token is checked.
	Account(ctx context.Context) (string, error)
	// Repos lists repositories, most recently active first.
	Repos(ctx context.Context) ([]Repo, error)
	// ReadFile returns a small file from a repo at a branch, or ErrNotFound.
	ReadFile(ctx context.Context, repo, branch, path string) ([]byte, error)
	// Archive writes the branch as a .tar.gz, stopping with ErrTooLarge past
	// limit bytes.
	Archive(ctx context.Context, repo, branch string, w io.Writer, limit int64) error
	// AddHook asks the host to call url on every push, signing with secret.
	// verifyTLS is false when Pail's certificate isn't one the host trusts.
	AddHook(ctx context.Context, repo, url, secret string, verifyTLS bool) (id string, err error)
	RemoveHook(ctx context.Context, repo, id string) error
}

// New makes a client. server is the host's address for a self-hosted one, or
// empty for the host's own.
func New(kind Kind, server, token string, hc *http.Client) (Client, error) {
	if server == "" {
		server = kind.DefaultServer()
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%q isn't a server address like https://git.home.example", server)
	}
	a := api{base: strings.TrimRight(server, "/"), hc: hc, auth: "Bearer " + token}
	switch kind {
	case GitHub:
		return github{a}, nil
	case GitLab:
		a.base += "/api/v4"
		return gitlab{a}, nil
	case Gitea, Forgejo:
		a.base += "/api/v1"
		a.auth = "token " + token
		return gitea{a}, nil
	case Bitbucket:
		// An app password is "username:password"; anything else is an access token.
		if user, pass, ok := strings.Cut(token, ":"); ok {
			a.auth, a.user, a.pass = "", user, pass
		}
		return bitbucket{a}, nil
	}
	return nil, fmt.Errorf("no git host called %q", kind)
}

// api is the plumbing every host shares: one base URL, one credential.
type api struct {
	base string
	hc   *http.Client
	auth string
	// user and pass are set instead of auth for HTTP basic.
	user, pass string
}

func (a api) do(ctx context.Context, method, target string, body any, header ...string) (*http.Response, error) {
	if !strings.HasPrefix(target, "http") {
		target = a.base + target
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	if a.auth != "" {
		req.Header.Set("Authorization", a.auth)
	} else {
		req.SetBasicAuth(a.user, a.pass)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode < 300:
		return resp, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		resp.Body.Close()
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNotFound
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	resp.Body.Close()
	return nil, fmt.Errorf("the git host answered %s: %s", resp.Status, strings.TrimSpace(string(detail)))
}

func (a api) json(ctx context.Context, method, target string, body, out any, header ...string) error {
	resp, err := a.do(ctx, method, target, body, header...)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// small is the most ReadFile will return: enough for any manifest.
const small = 1 << 20

func (a api) file(ctx context.Context, target string, header ...string) ([]byte, error) {
	resp, err := a.do(ctx, http.MethodGet, target, nil, header...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, small))
}

func (a api) archive(ctx context.Context, target string, w io.Writer, limit int64) error {
	resp, err := a.do(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return ErrTooLarge
	}
	return nil
}

// DefaultClient is the HTTP client Pail talks to git hosts with.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute}
}

// segs escapes each part of an owner/repo or file path for a URL.
func segs(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

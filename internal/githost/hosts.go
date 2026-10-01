package githost

import (
	"context"
	"fmt"
	"io"
	"net/url"
)

// GitHub

type github struct{ api }

func (g github) Account(ctx context.Context) (string, error) {
	var u struct{ Login string }
	return u.Login, g.json(ctx, "GET", "/user", nil, &u)
}

func (g github) Repos(ctx context.Context) ([]Repo, error) {
	var list []struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool
	}
	if err := g.json(ctx, "GET", "/user/repos?sort=pushed&per_page=100", nil, &list); err != nil {
		return nil, err
	}
	repos := make([]Repo, len(list))
	for i, r := range list {
		repos[i] = Repo{Full: r.FullName, Branch: r.DefaultBranch, Private: r.Private}
	}
	return repos, nil
}

func (g github) ReadFile(ctx context.Context, repo, branch, path string) ([]byte, error) {
	return g.file(ctx, "/repos/"+segs(repo)+"/contents/"+segs(path)+"?ref="+url.QueryEscape(branch), "Accept", "application/vnd.github.raw+json")
}

func (g github) Archive(ctx context.Context, repo, branch string, w io.Writer, limit int64) error {
	return g.archive(ctx, "/repos/"+segs(repo)+"/tarball/"+url.PathEscape(branch), w, limit)
}

func (g github) AddHook(ctx context.Context, repo, hookURL, secret string, verifyTLS bool) (string, error) {
	insecure := "0"
	if !verifyTLS {
		insecure = "1"
	}
	var out struct{ ID int64 }
	err := g.json(ctx, "POST", "/repos/"+segs(repo)+"/hooks", map[string]any{
		"name": "web", "active": true, "events": []string{"push"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret, "insecure_ssl": insecure},
	}, &out)
	return fmt.Sprint(out.ID), err
}

func (g github) RemoveHook(ctx context.Context, repo, id string) error {
	return g.json(ctx, "DELETE", "/repos/"+segs(repo)+"/hooks/"+url.PathEscape(id), nil, nil)
}

// GitLab addresses a project by its full path, escaped whole.

type gitlab struct{ api }

func (g gitlab) project(repo string) string { return "/projects/" + url.PathEscape(repo) }

func (g gitlab) Account(ctx context.Context) (string, error) {
	var u struct{ Username string }
	return u.Username, g.json(ctx, "GET", "/user", nil, &u)
}

func (g gitlab) Repos(ctx context.Context) ([]Repo, error) {
	var list []struct {
		Path          string `json:"path_with_namespace"`
		DefaultBranch string `json:"default_branch"`
		Visibility    string
	}
	if err := g.json(ctx, "GET", "/projects?membership=true&simple=true&order_by=last_activity_at&per_page=100", nil, &list); err != nil {
		return nil, err
	}
	repos := make([]Repo, len(list))
	for i, r := range list {
		repos[i] = Repo{Full: r.Path, Branch: r.DefaultBranch, Private: r.Visibility != "public"}
	}
	return repos, nil
}

func (g gitlab) ReadFile(ctx context.Context, repo, branch, path string) ([]byte, error) {
	return g.file(ctx, g.project(repo)+"/repository/files/"+url.PathEscape(path)+"/raw?ref="+url.QueryEscape(branch))
}

func (g gitlab) Archive(ctx context.Context, repo, branch string, w io.Writer, limit int64) error {
	return g.archive(ctx, g.project(repo)+"/repository/archive.tar.gz?sha="+url.QueryEscape(branch), w, limit)
}

func (g gitlab) AddHook(ctx context.Context, repo, hookURL, secret string, verifyTLS bool) (string, error) {
	var out struct{ ID int64 }
	err := g.json(ctx, "POST", g.project(repo)+"/hooks", map[string]any{
		"url": hookURL, "push_events": true, "token": secret, "enable_ssl_verification": verifyTLS,
	}, &out)
	return fmt.Sprint(out.ID), err
}

func (g gitlab) RemoveHook(ctx context.Context, repo, id string) error {
	return g.json(ctx, "DELETE", g.project(repo)+"/hooks/"+url.PathEscape(id), nil, nil)
}

// Gitea and Forgejo share one API.

type gitea struct{ api }

func (g gitea) Account(ctx context.Context) (string, error) {
	var u struct{ Login string }
	return u.Login, g.json(ctx, "GET", "/user", nil, &u)
}

func (g gitea) Repos(ctx context.Context) ([]Repo, error) {
	var list []struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool
	}
	if err := g.json(ctx, "GET", "/user/repos?limit=50", nil, &list); err != nil {
		return nil, err
	}
	repos := make([]Repo, len(list))
	for i, r := range list {
		repos[i] = Repo{Full: r.FullName, Branch: r.DefaultBranch, Private: r.Private}
	}
	return repos, nil
}

func (g gitea) ReadFile(ctx context.Context, repo, branch, path string) ([]byte, error) {
	return g.file(ctx, "/repos/"+segs(repo)+"/raw/"+segs(path)+"?ref="+url.QueryEscape(branch))
}

func (g gitea) Archive(ctx context.Context, repo, branch string, w io.Writer, limit int64) error {
	return g.archive(ctx, "/repos/"+segs(repo)+"/archive/"+url.PathEscape(branch)+".tar.gz", w, limit)
}

func (g gitea) AddHook(ctx context.Context, repo, hookURL, secret string, _ bool) (string, error) {
	var out struct{ ID int64 }
	err := g.json(ctx, "POST", "/repos/"+segs(repo)+"/hooks", map[string]any{
		"type": "gitea", "active": true, "events": []string{"push"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret},
	}, &out)
	return fmt.Sprint(out.ID), err
}

func (g gitea) RemoveHook(ctx context.Context, repo, id string) error {
	return g.json(ctx, "DELETE", "/repos/"+segs(repo)+"/hooks/"+url.PathEscape(id), nil, nil)
}

// Bitbucket Cloud

type bitbucket struct{ api }

func (b bitbucket) Account(ctx context.Context) (string, error) {
	var u struct{ Username, DisplayName string }
	err := b.json(ctx, "GET", "/2.0/user", nil, &u)
	if err == nil {
		if u.Username != "" {
			return u.Username, nil
		}
		return u.DisplayName, nil
	}
	// A workspace or repository access token has no user behind it, but it
	// can still list what it was made for.
	if _, listErr := b.Repos(ctx); listErr != nil {
		return "", err
	}
	return "", nil
}

func (b bitbucket) Repos(ctx context.Context) ([]Repo, error) {
	var page struct {
		Values []struct {
			FullName   string `json:"full_name"`
			IsPrivate  bool   `json:"is_private"`
			MainBranch struct{ Name string }
		}
	}
	if err := b.json(ctx, "GET", "/2.0/repositories?role=member&sort=-updated_on&pagelen=100", nil, &page); err != nil {
		return nil, err
	}
	repos := make([]Repo, len(page.Values))
	for i, r := range page.Values {
		repos[i] = Repo{Full: r.FullName, Branch: r.MainBranch.Name, Private: r.IsPrivate}
	}
	return repos, nil
}

func (b bitbucket) ReadFile(ctx context.Context, repo, branch, path string) ([]byte, error) {
	return b.file(ctx, "/2.0/repositories/"+segs(repo)+"/src/"+url.PathEscape(branch)+"/"+segs(path))
}

func (b bitbucket) Archive(ctx context.Context, repo, branch string, w io.Writer, limit int64) error {
	// Archives come from the website, not the API.
	site := "https://bitbucket.org"
	if b.base != Bitbucket.DefaultServer() {
		site = b.base
	}
	return b.archive(ctx, site+"/"+segs(repo)+"/get/"+url.PathEscape(branch)+".tar.gz", w, limit)
}

func (b bitbucket) AddHook(ctx context.Context, repo, hookURL, secret string, _ bool) (string, error) {
	var out struct{ UUID string }
	err := b.json(ctx, "POST", "/2.0/repositories/"+segs(repo)+"/hooks", map[string]any{
		"description": "Pail", "url": hookURL, "active": true, "secret": secret, "events": []string{"repo:push"},
	}, &out)
	return out.UUID, err
}

func (b bitbucket) RemoveHook(ctx context.Context, repo, id string) error {
	return b.json(ctx, "DELETE", "/2.0/repositories/"+segs(repo)+"/hooks/"+url.PathEscape(id), nil, nil)
}

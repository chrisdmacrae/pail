package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/githost"
	"github.com/chrisdmacrae/pail/internal/pails"
)

// apiGitHost is one git host as the UI's New pail screen needs it.
type apiGitHost struct {
	Kind  githost.Kind `json:"kind"`
	Label string       `json:"label"`
	// SelfHostable says connecting asks for the server's address.
	SelfHostable  bool   `json:"self_hostable"`
	DefaultServer string `json:"default_server"`
	Connected     bool   `json:"connected"`
	Server        string `json:"server,omitempty"`
	Account       string `json:"account,omitempty"`
	// OAuth says "Sign in with ..." is available: the server has an OAuth
	// app set up for this host.
	OAuth bool `json:"oauth"`
	// Via is how the host was connected: "token" or "oauth".
	Via string `json:"via,omitempty"`
}

func (s *Server) gitHost(kind githost.Kind) apiGitHost {
	h := apiGitHost{Kind: kind, Label: kind.Label(), SelfHostable: kind.SelfHostable()}
	if kind.SelfHostable() {
		h.DefaultServer = kind.DefaultServer()
	}
	if _, err := s.git.App(kind); err == nil {
		h.OAuth = true
	}
	if conn, ok := s.git.Get(kind); ok {
		h.Connected, h.Server, h.Account, h.Via = true, conn.Server, conn.Account, "token"
		if conn.OAuth {
			h.Via = "oauth"
		}
	}
	return h
}

// gitKind reads the {kind} in the path, answering 404 itself for a host
// Pail doesn't know.
func (s *Server) gitKind(w http.ResponseWriter, r *http.Request) (githost.Kind, bool) {
	kind := githost.Kind(r.PathValue("kind"))
	if !kind.Valid() {
		writeError(w, http.StatusNotFound, "no_git_host", "Pail doesn’t know a git host called "+string(kind)+".")
		return "", false
	}
	return kind, true
}

func (s *Server) handleListGit(w http.ResponseWriter, r *http.Request) {
	hosts := make([]apiGitHost, len(githost.Kinds))
	for i, kind := range githost.Kinds {
		hosts[i] = s.gitHost(kind)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

// handleConnectGit connects a git host: {"token": "...", "server": "..."}.
// The token is checked with the host before it is kept.
func (s *Server) handleConnectGit(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	var body struct {
		Server string `json:"server"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Paste an access token to connect "+kind.Label()+".")
		return
	}
	if _, err := s.git.Connect(r.Context(), kind, body.Server, body.Token); err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	writeJSON(w, http.StatusOK, s.gitHost(kind))
}

// A sign-in waits this long for the person to come back from the git host.
const signInWindow = 10 * time.Minute

// signIn is a sign-in under way: which host, and where it sends people back.
type signIn struct {
	kind        githost.Kind
	redirectURI string
	started     time.Time
}

// handleStartOAuth begins signing in to a git host. It answers with the
// host's address to send the browser to. The state it puts in that address
// is how the callback, which carries no Pail token, is known to be the end
// of a sign-in someone with the token began.
func (s *Server) handleStartOAuth(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	app, err := s.git.App(kind)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	state := randomID()
	in := signIn{kind: kind, redirectURI: origin(r, hostname(r.Host)) + "/oauth/callback/" + string(kind), started: time.Now()}
	s.signInsMu.Lock()
	for old, v := range s.signIns {
		if time.Since(v.started) > signInWindow {
			delete(s.signIns, old)
		}
	}
	s.signIns[state] = in
	s.signInsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"url": githost.AuthorizeURL(kind, app, in.redirectURI, state)})
}

// handleOAuthCallback is where a git host sends the browser back after a
// sign-in. Whatever happens, the person lands on New pail, with the host
// selected and anything that went wrong said there.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	kind := githost.Kind(r.PathValue("kind"))
	back := func(problem string) {
		q := url.Values{"source": {string(kind)}}
		if problem != "" {
			q.Set("error", problem)
		}
		http.Redirect(w, r, "/new?"+q.Encode(), http.StatusSeeOther)
	}

	state := r.URL.Query().Get("state")
	s.signInsMu.Lock()
	in, ok := s.signIns[state]
	delete(s.signIns, state) // a state is good once
	s.signInsMu.Unlock()
	switch {
	case !ok || in.kind != kind || time.Since(in.started) > signInWindow:
		back("That sign-in didn’t start here, or took too long. Try again.")
		return
	case r.URL.Query().Get("error") != "":
		back(kind.Label() + " didn’t sign you in: " + cmp.Or(r.URL.Query().Get("error_description"), r.URL.Query().Get("error")) + ".")
		return
	}
	if _, err := s.git.ConnectOAuth(r.Context(), kind, r.URL.Query().Get("code"), in.redirectURI); err != nil {
		s.log.Error("oauth sign-in", "host", kind, "err", err)
		back("Pail couldn’t finish signing in to " + kind.Label() + ": " + err.Error() + ".")
		return
	}
	back("")
}

func (s *Server) handleDisconnectGit(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	if err := s.git.Disconnect(r.Context(), kind); err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	client, err := s.git.Client(r.Context(), kind)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	repos, err := client.Repos(r.Context())
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	if repos == nil {
		repos = []githost.Repo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// handleDetectRepo says what Pail makes of a repo: ?repo=owner/name&branch=main.
func (s *Server) handleDetectRepo(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	repo, branch := r.URL.Query().Get("repo"), r.URL.Query().Get("branch")
	if repo == "" || branch == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Say which repo and branch: ?repo=owner/name&branch=main.")
		return
	}
	client, err := s.git.Client(r.Context(), kind)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	found, err := githost.Detect(r.Context(), client, repo, branch)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

// deployFromGit fetches a pail's branch from its git host and starts a
// deploy of it, the same way an upload would.
func (s *Server) deployFromGit(ctx context.Context, r *http.Request, name string, git pails.GitSource, label string) (pails.Deploy, error) {
	kind := githost.Kind(git.Host)
	client, err := s.git.Client(ctx, kind)
	if err != nil {
		return pails.Deploy{}, err
	}
	tmp, err := os.CreateTemp("", "pail-git-*")
	if err != nil {
		return pails.Deploy{}, err
	}
	err = client.Archive(ctx, git.Repo, git.Branch, tmp, s.cfg.MaxUploadSize)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return pails.Deploy{}, err
	}
	return s.pails.StartDeploy(name, pails.Upload{
		Path: tmp.Name(), Source: git.Host, Label: label, URL: origin(r, name+"."+s.cfg.BaseDomain),
	})
}

// handleCreateFromRepo makes a new pail from a repo:
// {"host": "forgejo", "repo": "homelab/recipes", "branch": "main"}. It
// deploys the branch as it stands, then asks the host to say when the branch
// is pushed to, so every push is a deploy.
func (s *Server) handleCreateFromRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pails.ValidName(name) {
		s.writePailError(w, r, pails.ErrBadName)
		return
	}
	var body struct {
		Host   githost.Kind `json:"host"`
		Repo   string       `json:"repo"`
		Branch string       `json:"branch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || !body.Host.Valid() || body.Repo == "" || body.Branch == "" {
		writeError(w, http.StatusBadRequest, "bad_request", `Say which repo to deploy: {"host": "github", "repo": "owner/name", "branch": "main"}.`)
		return
	}
	kind := body.Host
	if _, err := s.pails.Get(name); err == nil {
		writeError(w, http.StatusConflict, "name_taken", name+" is already a pail. Pick another name.")
		return
	}
	client, err := s.git.Client(r.Context(), kind)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	found, err := githost.Detect(r.Context(), client, body.Repo, body.Branch)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	if !found.Deployable {
		writeError(w, http.StatusConflict, "not_deployable", "Pail can’t deploy "+body.Repo+" as it is: "+found.Summary+".")
		return
	}

	git := pails.GitSource{Host: string(kind), Repo: body.Repo, Branch: body.Branch, HookSecret: randomID()}
	d, err := s.deployFromGit(r.Context(), r, name, git, "first deploy from "+body.Repo+"@"+body.Branch)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}

	// The pail exists now. A webhook that can't be added doesn't undo it:
	// the pail works, and Redeploy pulls the branch by hand.
	hookURL := origin(r, s.cfg.BaseDomain) + "/api/v1/hooks/" + name
	hookNote := ""
	if git.HookID, err = client.AddHook(r.Context(), git.Repo, hookURL, git.HookSecret, s.TLSMode() != "internal"); err != nil {
		s.log.Warn("add webhook", "pail", name, "repo", git.Repo, "err", err)
		hookNote = "Pail couldn’t add a webhook to " + body.Repo + ", so pushes won’t deploy by themselves. Use Redeploy after a push, or give the token permission to add webhooks and make the pail again."
	}
	if err := s.pails.SetGit(r.Context(), name, &git); err != nil {
		s.writePailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		apiDeploy
		// Hook says pushes to the branch will deploy by themselves.
		Hook     bool   `json:"hook"`
		HookNote string `json:"hook_note,omitempty"`
	}{s.deployJSON(r, name, d, ""), hookNote == "", hookNote})
}

// handleHook receives a git host's webhook for a pail. It carries no Pail
// token: the host signs it with the secret Pail gave it for this hook.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	git, err := s.pails.Git(name)
	if err != nil || git == nil || git.HookSecret == "" {
		writeError(w, http.StatusNotFound, "no_hook", "No pail here takes webhooks.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That delivery is too big to read.")
		return
	}
	push := githost.ReadPush(githost.Kind(git.Host), git.HookSecret, r.Header, body)
	switch {
	case !push.Genuine:
		writeError(w, http.StatusUnauthorized, "bad_signature", "That delivery isn’t signed with this hook’s secret.")
		return
	case push.Branch != git.Branch:
		// A ping, a tag, or a push to some other branch: nothing to deploy.
		writeJSON(w, http.StatusOK, map[string]any{"deployed": false})
		return
	}
	d, err := s.deployFromGit(r.Context(), r, name, *git, "push to "+git.Branch)
	if err != nil {
		s.log.Error("deploy from webhook", "pail", name, "err", err)
		s.writeGitError(w, r, githost.Kind(git.Host), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"deployed": true, "deploy": d.ID})
}

// removeHook takes Pail's webhook off a repo when its pail goes. It's a
// courtesy: a hook left behind only gets a 404.
func (s *Server) removeHook(ctx context.Context, git *pails.GitSource) {
	if git == nil || git.HookID == "" {
		return
	}
	client, err := s.git.Client(ctx, githost.Kind(git.Host))
	if err != nil {
		return
	}
	if err := client.RemoveHook(ctx, git.Repo, git.HookID); err != nil {
		s.log.Warn("remove webhook", "repo", git.Repo, "err", err)
	}
}

func (s *Server) writeGitError(w http.ResponseWriter, r *http.Request, kind githost.Kind, err error) {
	label := kind.Label()
	switch {
	case errors.Is(err, githost.ErrNotConnected):
		writeError(w, http.StatusConflict, "not_connected", label+" isn’t connected. Connect it from New pail with an access token.")
	case errors.Is(err, githost.ErrNoOAuth):
		prefix := "PAIL_OAUTH_" + strings.ToUpper(string(kind))
		writeError(w, http.StatusConflict, "no_oauth", "This Pail has no OAuth app for "+label+". Set "+prefix+"_CLIENT_ID and "+prefix+"_CLIENT_SECRET on the server, or connect with a token.")
	case errors.Is(err, githost.ErrNoServer):
		writeError(w, http.StatusBadRequest, "no_server", label+" needs its server’s address, like https://git.home.example.")
	case errors.Is(err, githost.ErrUnauthorized):
		writeError(w, http.StatusUnprocessableEntity, "token_rejected", label+" didn’t accept that token. Check it’s current and can read your repos.")
	case errors.Is(err, githost.ErrNotFound):
		writeError(w, http.StatusNotFound, "no_repo", label+" has no such repo or branch, or the token can’t see it.")
	case errors.Is(err, githost.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That repo is over "+config.FormatSize(s.cfg.MaxUploadSize)+", the most this Pail takes. Raise PAIL_MAX_UPLOAD_SIZE on the server to deploy it.")
	case errors.Is(err, pails.ErrNoPail), errors.Is(err, pails.ErrBadName), errors.Is(err, pails.ErrNotArchive), errors.Is(err, pails.ErrBusy):
		s.writePailError(w, r, err)
	default:
		s.log.Error("git host", "host", kind, "path", path.Clean(r.URL.Path), "err", err)
		writeError(w, http.StatusBadGateway, "git_host", "Pail couldn’t reach "+label+": "+err.Error()+".")
	}
}

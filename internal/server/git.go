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
	// OAuth says "Sign in with ..." is available: the server has an OAuth
	// app set up for this host.
	OAuth bool `json:"oauth"`
}

// apiGitConnection is a connection to a git host: one waiting for its pail
// under an id, or one a pail was just given, which has no id any more. It
// never carries the token: the id stands for it.
type apiGitConnection struct {
	ID      string       `json:"id,omitempty"`
	Kind    githost.Kind `json:"kind"`
	Server  string       `json:"server,omitempty"`
	Account string       `json:"account,omitempty"`
	// Via is how it was made: "token" or "oauth".
	Via string `json:"via"`
}

func gitConnection(id string, conn githost.Connection) apiGitConnection {
	c := apiGitConnection{ID: id, Kind: conn.Kind, Server: conn.Server, Account: conn.Account, Via: "token"}
	if conn.OAuth {
		c.Via = "oauth"
	}
	return c
}

func (s *Server) gitHost(kind githost.Kind) apiGitHost {
	h := apiGitHost{Kind: kind, Label: kind.Label(), SelfHostable: kind.SelfHostable()}
	if kind.SelfHostable() {
		h.DefaultServer = kind.DefaultServer()
	}
	if _, err := s.git.App(kind); err == nil {
		h.OAuth = true
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

// handleConnectGit connects a git host for the pail about to be made:
// {"token": "...", "server": "..."}. The token is checked with the host, then
// held under the id this answers with until a pail is made with it.
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
	id, conn, err := s.git.Connect(r.Context(), kind, body.Server, body.Token)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	writeJSON(w, http.StatusOK, gitConnection(id, conn))
}

// handleGetConnection describes a connection that is waiting for its pail.
func (s *Server) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	conn, ok := s.git.Waiting(kind, r.PathValue("id"))
	if !ok {
		s.writeGitError(w, r, kind, githost.ErrNotConnected)
		return
	}
	writeJSON(w, http.StatusOK, gitConnection(r.PathValue("id"), conn))
}

// handleDropConnection forgets a connection that is waiting for its pail.
func (s *Server) handleDropConnection(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	if _, ok := s.git.Waiting(kind, r.PathValue("id")); ok {
		s.git.Drop(r.PathValue("id"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// A sign-in waits this long for the person to come back from the git host.
const signInWindow = 10 * time.Minute

// signIn is a sign-in under way: which host, and where it sends people back.
type signIn struct {
	kind        githost.Kind
	redirectURI string
	started     time.Time
	// pail is the pail the sign-in is to reconnect, or "" when it is for a
	// pail about to be made.
	pail string
}

// handleStartOAuth begins signing in to a git host. It answers with the
// host's address to send the browser to. The state it puts in that address
// is how the callback, which carries no Pail token, is known to be the end
// of a sign-in someone with the token began. With {"pail": "recipes"} the
// sign-in is to reconnect that pail, and comes back to its page rather than
// to New pail.
func (s *Server) handleStartOAuth(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	var body struct {
		Pail string `json:"pail"`
	}
	// No body at all is a sign-in for a new pail.
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	if body.Pail != "" {
		r.SetPathValue("name", body.Pail)
		git, ok := s.pailGit(w, r)
		if !ok {
			return
		}
		if git.Host != string(kind) {
			writeError(w, http.StatusConflict, "other_git_host", body.Pail+" deploys from "+githost.Kind(git.Host).Label()+", not "+kind.Label()+". Sign in there to reconnect it.")
			return
		}
	}
	app, err := s.git.App(kind)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	state := randomID()
	in := signIn{kind: kind, redirectURI: origin(r, hostname(r.Host)) + "/oauth/callback/" + string(kind), started: time.Now(), pail: body.Pail}
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
// sign-in. Whatever happens, the person lands where the sign-in began, with
// either the connection it made or what went wrong: on the page of the pail
// it is to reconnect, or else on New pail with the host selected.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	kind := githost.Kind(r.PathValue("kind"))
	state := r.URL.Query().Get("state")
	s.signInsMu.Lock()
	in, ok := s.signIns[state]
	delete(s.signIns, state) // a state is good once
	s.signInsMu.Unlock()

	arrive := func(with, value string) {
		to, q := "/new", url.Values{"source": {string(kind)}, with: {value}}
		if in.pail != "" {
			to, q = "/pails/"+url.PathEscape(in.pail), url.Values{with: {value}}
		}
		http.Redirect(w, r, to+"?"+q.Encode(), http.StatusSeeOther)
	}
	back := func(problem string) { arrive("error", problem) }
	switch {
	case !ok || in.kind != kind || time.Since(in.started) > signInWindow:
		back("That sign-in didn’t start here, or took too long. Try again.")
		return
	case r.URL.Query().Get("error") != "":
		back(kind.Label() + " didn’t sign you in: " + cmp.Or(r.URL.Query().Get("error_description"), r.URL.Query().Get("error")) + ".")
		return
	}
	id, _, err := s.git.ConnectOAuth(r.Context(), kind, r.URL.Query().Get("code"), in.redirectURI)
	if err != nil {
		s.log.Error("oauth sign-in", "host", kind, "err", err)
		back("Pail couldn’t finish signing in to " + kind.Label() + ": " + err.Error() + ".")
		return
	}
	arrive("connection", id)
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.gitKind(w, r)
	if !ok {
		return
	}
	client, err := s.git.WaitingClient(r.Context(), kind, r.URL.Query().Get("connection"))
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

// repoDir reads the folder of a repo a pail deploys from, answering 400
// itself for one that isn't inside the repo.
func repoDir(w http.ResponseWriter, dir string) (string, bool) {
	clean, err := pails.CleanDir(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "The folder has to be one inside the repo, like apps/web.")
		return "", false
	}
	return clean, true
}

// handleDetectRepo says what Pail makes of a repo, as the connection in
// ?connection= sees it: ?repo=owner/name&branch=main. With &dir=apps/web it
// looks in that folder rather than at the top.
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
	dir, ok := repoDir(w, r.URL.Query().Get("dir"))
	if !ok {
		return
	}
	client, err := s.git.WaitingClient(r.Context(), kind, r.URL.Query().Get("connection"))
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	found, err := githost.Detect(r.Context(), client, repo, branch, dir, s.canBuild())
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

// canBuild says whether this Pail can build a project before serving it.
func (s *Server) canBuild() bool {
	ok, _ := s.pails.CanBuild()
	return ok
}

// deployFromGit fetches a pail's branch from its git host and starts a
// deploy of it, the same way an upload would. It pulls with client, or with
// the pail's own connection when client is nil.
func (s *Server) deployFromGit(ctx context.Context, r *http.Request, client githost.Client, name string, git pails.GitSource, label string) (pails.Deploy, error) {
	if client == nil {
		var err error
		if client, err = s.git.Client(ctx, name, githost.Kind(git.Host)); err != nil {
			return pails.Deploy{}, err
		}
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
		Path: tmp.Name(), Source: git.Host, Label: label, URL: origin(r, name+"."+s.cfg.BaseDomain), Dir: git.Dir,
	})
}

// handleCreateFromRepo makes a new pail from a repo:
// {"host": "forgejo", "connection": "...", "repo": "homelab/recipes",
// "branch": "main"}. It deploys the branch as it stands, then asks the host
// to say when the branch is pushed to, so every push is a deploy. With
// "dir": "apps/web" the pail is that folder of the repo, which lets one repo
// hold several pails. The connection becomes the pail's own: the next pail
// is made with another.
func (s *Server) handleCreateFromRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pails.ValidName(name) {
		s.writePailError(w, r, pails.ErrBadName)
		return
	}
	var body struct {
		Host       githost.Kind `json:"host"`
		Connection string       `json:"connection"`
		Repo       string       `json:"repo"`
		Branch     string       `json:"branch"`
		Dir        string       `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || !body.Host.Valid() || body.Repo == "" || body.Branch == "" {
		writeError(w, http.StatusBadRequest, "bad_request", `Say which repo to deploy: {"host": "github", "connection": "...", "repo": "owner/name", "branch": "main"}.`)
		return
	}
	kind := body.Host
	dir, ok := repoDir(w, body.Dir)
	if !ok {
		return
	}
	if _, err := s.pails.Get(name); err == nil {
		writeError(w, http.StatusConflict, "name_taken", name+" is already a pail. Pick another name.")
		return
	}
	client, err := s.git.WaitingClient(r.Context(), kind, body.Connection)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	found, err := githost.Detect(r.Context(), client, body.Repo, body.Branch, dir, s.canBuild())
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	if !found.Deployable {
		writeError(w, http.StatusConflict, "not_deployable", "Pail can’t deploy "+body.Repo+" as it is: "+found.Summary+".")
		return
	}

	git := pails.GitSource{Host: string(kind), Repo: body.Repo, Branch: body.Branch, Dir: dir, HookSecret: randomID()}
	from := body.Repo + "@" + body.Branch
	if dir != "" {
		from += ":" + dir
	}
	d, err := s.deployFromGit(r.Context(), r, client, name, git, "first deploy from "+from)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	// The pail exists now, and the connection is its own from here on.
	if err := s.git.Give(r.Context(), body.Connection, name); err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}

	// A webhook that can't be added doesn't undo the pail: it works, and
	// Redeploy pulls the branch by hand.
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

// pailGit reads the repo the pail in the path deploys from, answering itself
// for a pail that isn't there or doesn't come from a git host.
func (s *Server) pailGit(w http.ResponseWriter, r *http.Request) (*pails.GitSource, bool) {
	name := r.PathValue("name")
	git, err := s.pails.Git(name)
	switch {
	case err != nil:
		s.writePailError(w, r, err)
		return nil, false
	case git == nil:
		writeError(w, http.StatusConflict, "not_from_git", name+" doesn’t deploy from a git host, so it has no connection to one.")
		return nil, false
	}
	return git, true
}

// handleReconnect gives a pail from a git host a new connection, for when
// the one it was made with has run out or been revoked:
// {"connection": "..."}. The connection is made the way New pail makes one,
// to the same kind of host, and has to reach the pail's repo. It takes the
// place of the pail's old one, and can't be used again.
func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	git, ok := s.pailGit(w, r)
	if !ok {
		return
	}
	kind := githost.Kind(git.Host)
	var body struct {
		Connection string `json:"connection"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Connection == "" {
		writeError(w, http.StatusBadRequest, "bad_request", `Say which connection to give the pail: {"connection": "<id>"}.`)
		return
	}
	conn, ok := s.git.Waiting(kind, body.Connection)
	if !ok {
		writeError(w, http.StatusConflict, "not_connected", "Pail has no connection to "+kind.Label()+" waiting under that id. Connect again, by signing in or with an access token.")
		return
	}
	client, err := s.git.WaitingClient(r.Context(), kind, body.Connection)
	if err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	// A deploy pulls the branch as an archive, so that is what the new
	// connection has to manage. One past the size limit was still seen.
	err = client.Archive(r.Context(), git.Repo, git.Branch, io.Discard, s.cfg.MaxUploadSize)
	switch {
	case errors.Is(err, githost.ErrNotFound):
		writeError(w, http.StatusConflict, "cant_see_repo", "That connection can’t see "+git.Repo+" on "+kind.Label()+", so "+name+" couldn’t pull with it. Connect as someone who can, or with a token that reaches the repo.")
		return
	case err != nil && !errors.Is(err, githost.ErrTooLarge):
		s.writeGitError(w, r, kind, err)
		return
	}
	if err := s.git.Give(r.Context(), body.Connection, name); err != nil {
		s.writeGitError(w, r, kind, err)
		return
	}
	writeJSON(w, http.StatusOK, gitConnection("", conn))
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
	// A pail that is one folder of its repo sits out a push that changed
	// nothing of its own.
	if git.Dir != "" && !push.Touches(git.Dir, s.watched(r.Context(), name, git)) {
		writeJSON(w, http.StatusOK, map[string]any{"deployed": false, "skipped": "The push changed nothing in " + git.Dir + "."})
		return
	}
	d, err := s.deployFromGit(r.Context(), r, nil, name, *git, "push to "+git.Branch)
	if err != nil {
		s.log.Error("deploy from webhook", "pail", name, "err", err)
		s.writeGitError(w, r, githost.Kind(git.Host), err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"deployed": true, "deploy": d.ID})
}

// watched reads the folders and files outside its own that a pail's pail.json
// asks to be redeployed for, as paths from the top of the repo. If they can't
// be read, everything is watched: the pail deploys.
func (s *Server) watched(ctx context.Context, name string, git *pails.GitSource) []string {
	everything := []string{""}
	client, err := s.git.Client(ctx, name, githost.Kind(git.Host))
	if err != nil {
		return everything
	}
	manifest, err := client.ReadFile(ctx, git.Repo, git.Branch, path.Join(git.Dir, "pail.json"))
	if errors.Is(err, githost.ErrNotFound) {
		return nil
	}
	watch, ok := pails.Watched(manifest)
	if err != nil || !ok {
		return everything
	}
	return watch
}

// removeHook takes Pail's webhook off a repo when its pail goes. It's a
// courtesy: a hook left behind only gets a 404.
func (s *Server) removeHook(ctx context.Context, name string, git *pails.GitSource) {
	if git == nil || git.HookID == "" {
		return
	}
	client, err := s.git.Client(ctx, name, githost.Kind(git.Host))
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
		writeError(w, http.StatusConflict, "not_connected", label+" isn’t connected. Connect it from New pail, by signing in or with an access token.")
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

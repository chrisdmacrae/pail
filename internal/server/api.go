package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/pails"
)

// installation serves the base domain: the REST API under /api/v1, and the
// web UI everywhere else.
func (s *Server) installation() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/info", s.handleInfo)
	api.HandleFunc("GET /api/v1/pails", s.handleListPails)
	api.HandleFunc("GET /api/v1/pails/{name}", s.handleGetPail)
	api.HandleFunc("DELETE /api/v1/pails/{name}", s.handleRemovePail)
	api.HandleFunc("GET /api/v1/pails/{name}/deploys", s.handleListDeploys)
	api.HandleFunc("POST /api/v1/pails/{name}/deploys", s.handleCreateDeploy)
	api.HandleFunc("POST /api/v1/pails/{name}/serve", s.handleServe)
	api.HandleFunc("POST /api/v1/pails/{name}/redeploy", s.handleRedeploy)
	api.HandleFunc("POST /api/v1/pails/{name}/stop", s.handleSetOff(true))
	api.HandleFunc("POST /api/v1/pails/{name}/start", s.handleSetOff(false))
	api.HandleFunc("GET /api/v1/pails/{name}/deploys/{id}/log", s.handleDeployLog)
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such API path: "+r.URL.Path+".")
	})

	mux := http.NewServeMux()
	mux.Handle("/api/", s.requireToken(api))
	mux.HandleFunc("/", s.serveUI)
	return mux
}

// requireToken checks the installation's one token on every API call.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.cfg.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "token_missing", "This Pail needs its token. Send it as an Authorization: Bearer header.")
			return
		}
		got := sha256.Sum256([]byte(strings.TrimSpace(token)))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "token_rejected", "That isn't this Pail's token. Use the one set as PAIL_TOKEN on the server.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type apiPail struct {
	pails.Pail
	URL string `json:"url"`
}

type apiDeploy struct {
	pails.Deploy
	Pail string `json:"pail"`
	// URL is the pail's address, where this deploy is or will be live.
	URL string `json:"url"`
	// Serving says this is the deploy requests are answered from.
	Serving bool `json:"serving"`
}

func (s *Server) deployJSON(r *http.Request, name string, d pails.Deploy, serving string) apiDeploy {
	return apiDeploy{Deploy: d, Pail: name, URL: origin(r, name+"."+s.cfg.BaseDomain), Serving: d.ID == serving}
}

func (s *Server) pailJSON(r *http.Request, p pails.Pail) apiPail {
	return apiPail{Pail: p, URL: origin(r, p.Host)}
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.version,
		"base_domain": s.cfg.BaseDomain,
		"limits": map[string]any{
			"max_upload_size":     s.cfg.MaxUploadSize,
			"max_deploys":         s.cfg.MaxDeploys,
			"max_function_memory": s.cfg.MaxFunctionMemory,
		},
	})
}

func (s *Server) handleListPails(w http.ResponseWriter, r *http.Request) {
	list := []apiPail{}
	for _, p := range s.pails.List() {
		list = append(list, s.pailJSON(r, p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pails": list})
}

func (s *Server) handleGetPail(w http.ResponseWriter, r *http.Request) {
	p, err := s.pails.Get(r.PathValue("name"))
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pailJSON(r, p))
}

func (s *Server) handleRemovePail(w http.ResponseWriter, r *http.Request) {
	if err := s.pails.Remove(r.Context(), r.PathValue("name")); err != nil {
		s.writePailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListDeploys lists the deploys a pail keeps, newest first.
func (s *Server) handleListDeploys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	deploys, serving, err := s.pails.Deploys(name)
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	list := []apiDeploy{}
	for _, d := range deploys {
		list = append(list, s.deployJSON(r, name, d, serving))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deploys": list, "serving": serving})
}

// handleServe points the pail at one of its kept deploys: {"deploy": id}.
func (s *Server) handleServe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Deploy string `json:"deploy"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Deploy == "" {
		writeError(w, http.StatusBadRequest, "bad_request", `Say which deploy to serve: {"deploy": "<id>"}.`)
		return
	}
	r.SetPathValue("id", body.Deploy)
	p, err := s.pails.Serve(r.Context(), r.PathValue("name"), body.Deploy)
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pailJSON(r, p))
}

// handleRedeploy starts a new deploy from the pail's latest good one. Like
// an upload, it answers at once and the deploy's log says how it went.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	d, err := s.pails.Redeploy(name, origin(r, name+"."+s.cfg.BaseDomain))
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.deployJSON(r, name, d, ""))
}

// handleSetOff stops a pail (off) or starts it again.
func (s *Server) handleSetOff(off bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.pails.SetOff(r.Context(), r.PathValue("name"), off)
		if err != nil {
			s.writePailError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, s.pailJSON(r, p))
	}
}

// handleCreateDeploy takes a .tar.gz or .zip as the request body and starts a
// deploy of it, creating the pail on its first. It answers as soon as the
// archive has arrived; the deploy's log says how it went.
func (s *Server) handleCreateDeploy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pails.ValidName(name) {
		s.writePailError(w, r, pails.ErrBadName)
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "upload"
	}
	if source != "cli" && source != "upload" {
		writeError(w, http.StatusBadRequest, "bad_source", `source is "cli" or "upload".`)
		return
	}

	limit := s.cfg.MaxUploadSize
	tooBig := fmt.Sprintf("That upload is over %s, the most this Pail takes. Raise PAIL_MAX_UPLOAD_SIZE on the server to send more.", config.FormatSize(limit))
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", tooBig)
		return
	}
	tmp, err := os.CreateTemp("", "pail-upload-*")
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, limit))
	tmp.Close()
	if err != nil || n == 0 {
		os.Remove(tmp.Name())
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", tooBig)
		case err != nil:
			writeError(w, http.StatusBadRequest, "upload_interrupted", "The upload stopped partway. Send it again.")
		default:
			writeError(w, http.StatusBadRequest, "empty_upload", "Nothing arrived. Send a .tar.gz or a .zip as the request body.")
		}
		return
	}

	label := "upload"
	if file := path.Base(strings.ReplaceAll(r.URL.Query().Get("file"), `\`, "/")); file != "." && file != "/" {
		label = "uploaded " + file
	}
	if source == "cli" {
		label = "pail up"
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			label += " from " + ip
		}
	}

	url := origin(r, name+"."+s.cfg.BaseDomain)
	d, err := s.pails.StartDeploy(name, pails.Upload{Path: tmp.Name(), Source: source, Label: label, URL: url})
	if err != nil {
		s.writePailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.deployJSON(r, name, d, ""))
}

// handleDeployLog streams a deploy's log as server-sent events: a "line"
// event per line, then one "done" event carrying the finished deploy. A
// deploy that already finished replays its log and ends the same way. With
// ?follow=false a deploy still building sends the lines so far and stops,
// without a "done".
func (s *Server) handleDeployLog(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	follow := r.URL.Query().Get("follow") != "false"
	lg, err := s.pails.Log(r.Context(), name, id)
	if err != nil {
		s.writePailError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher := http.NewResponseController(w)
	event := func(name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}

	sent := 0
	for {
		lines, done, wake := lg.Since(sent)
		for _, line := range lines {
			event("line", line)
		}
		sent += len(lines)
		if done {
			// The pail may have been removed under us; say so with what's left.
			d, _ := s.pails.Deploy(name, id)
			d.ID = id
			_, serving, _ := s.pails.Deploys(name)
			event("done", s.deployJSON(r, name, d, serving))
			flusher.Flush()
			return
		}
		flusher.Flush()
		if !follow {
			return
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) writePailError(w http.ResponseWriter, r *http.Request, err error) {
	name := r.PathValue("name")
	switch {
	case errors.Is(err, pails.ErrNoPail):
		writeError(w, http.StatusNotFound, "no_pail", "No pail called "+name+".")
	case errors.Is(err, pails.ErrNoDeploy):
		writeError(w, http.StatusNotFound, "no_deploy", name+" has no deploy "+r.PathValue("id")+". It may have been cleared to make room for newer ones.")
	case errors.Is(err, pails.ErrBadName):
		writeError(w, http.StatusBadRequest, "bad_name", "Pail names are lowercase letters, numbers and dashes, like my-site.")
	case errors.Is(err, pails.ErrNotArchive):
		writeError(w, http.StatusUnsupportedMediaType, "not_an_archive", "That isn't a .tar.gz or a .zip. Pack the folder and send it again.")
	case errors.Is(err, pails.ErrBuilding):
		writeError(w, http.StatusConflict, "building", name+" has a deploy running. Wait for it to finish, then try again.")
	case errors.Is(err, pails.ErrNotServable):
		writeError(w, http.StatusConflict, "not_servable", "Deploy "+r.PathValue("id")+" didn't finish, so there's nothing to serve. Pick one that did.")
	case errors.Is(err, pails.ErrNoSource):
		writeError(w, http.StatusConflict, "nothing_to_redeploy", name+" has no finished deploy to redeploy. Send the files again with pail up.")
	case errors.Is(err, pails.ErrBusy):
		writeError(w, http.StatusConflict, "being_removed", name+" is still being removed. Try again in a moment.")
	default:
		s.log.Error("api", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Pail hit a problem of its own. The server log says what.")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

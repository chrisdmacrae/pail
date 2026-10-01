package server

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/internal/pails"
)

// serveSite answers a request for a pail's own host from its live deploy.
func (s *Server) serveSite(w http.ResponseWriter, r *http.Request, name string) {

	// The live pointer is read here, once. The rest of the request is served
	// from this deploy even if a newer one goes live meanwhile.
	live, err := s.pails.Live(r.Context(), name)
	switch {
	case errors.Is(err, pails.ErrNoPail):
		s.notFound(w, r, "No pail called "+name+".")
		return
	case errors.Is(err, pails.ErrOff):
		s.plainPage(w, r, http.StatusServiceUnavailable, name+" is off. Start it with pail start "+name+".")
		return
	case errors.Is(err, pails.ErrNothingLive):
		s.notFound(w, r, name+" has nothing live yet.")
		return
	case err != nil:
		s.log.Error("site", "pail", name, "err", err)
		http.Error(w, "Pail couldn't read this pail's files.", http.StatusInternalServerError)
		return
	}
	// Routes decide what answers: a container, a function, or the deploy's
	// files.
	target, ok := live.Manifest.Match(path.Clean("/" + r.URL.Path))
	if !ok {
		s.notFound(w, r, "Nothing at "+r.URL.Path+" in "+name+".")
		return
	}
	if target.Container != "" {
		s.serveContainer(w, r, live, target.Container)
		return
	}
	if target.Function != "" {
		s.serveFunction(w, r, live, target.Function)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "This path only serves files.", http.StatusMethodNotAllowed)
		return
	}
	files := live.Manifest.Files

	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	wantsDir := p == "" || strings.HasSuffix(r.URL.Path, "/")

	status := http.StatusOK
	file := ""
	ok = false
	switch {
	case wantsDir:
		file = path.Join(p, "index.html")
		_, ok = files[file]
	case has(files, p):
		file, ok = p, true
	case has(files, p+"/index.html"):
		// A folder asked for without its slash: relative links inside need it.
		target := "/" + p + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	case has(files, p+".html"):
		file, ok = p+".html", true
	}
	if !ok && live.Manifest.Fallback != "" {
		file, ok = live.Manifest.Fallback, true
	}
	if !ok {
		if !has(files, "404.html") {
			s.notFound(w, r, "Nothing at "+r.URL.Path+" in "+name+".")
			return
		}
		file, status = "404.html", http.StatusNotFound
	}

	info := files[file]
	body, err := s.pails.Open(r.Context(), live, file)
	if err != nil {
		s.log.Error("site", "pail", name, "deploy", live.Deploy, "file", file, "err", err)
		http.Error(w, "Pail couldn't read this file.", http.StatusInternalServerError)
		return
	}
	defer body.Close()

	h := w.Header()
	h.Set("Content-Type", info.Type)
	h.Set("X-Content-Type-Options", "nosniff")
	// Always revalidate: a deploy swaps every file at once, and the ETag
	// makes an unchanged file a 304.
	h.Set("Cache-Control", "no-cache")
	if status != http.StatusOK {
		h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			io.Copy(w, body)
		}
		return
	}
	h.Set("ETag", `"`+info.ETag+`"`)
	http.ServeContent(w, r, "", time.Time{}, body)
}

func has(files map[string]pails.File, name string) bool {
	_, ok := files[name]
	return ok
}

package server

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The UI's page may load only its own scripts, styles and fonts, and talk
// only to this installation: it holds the installation's token.
const uiPolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// serveUI answers everything on the installation's host that isn't the API:
// the web UI's files, and its page for any path the UI routes itself.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		if r.URL.Path != "/" {
			s.notFound(w, r, "Nothing at "+r.URL.Path+".")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Pail is running on %s. The API is at /api/v1. This build has no web UI; run make ui and build again.\n", s.cfg.BaseDomain)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Pail's UI only serves files.", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if info, err := fs.Stat(s.ui, name); name != "" && err == nil && !info.IsDir() {
		// Built assets carry a hash of their contents in their names.
		cache := "no-cache"
		if strings.HasPrefix(name, "assets/") {
			cache = "public, max-age=31536000, immutable"
		}
		s.serveUIFile(w, r, name, cache)
		return
	}
	if path.Ext(name) != "" {
		s.notFound(w, r, "Nothing at "+r.URL.Path+".")
		return
	}
	// A path the UI routes itself, like /pails/blog: its one page.
	w.Header().Set("Content-Security-Policy", uiPolicy)
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.serveUIFile(w, r, "index.html", "no-cache")
}

func (s *Server) serveUIFile(w http.ResponseWriter, r *http.Request, name, cache string) {
	f, err := s.ui.Open(name)
	if err != nil {
		s.notFound(w, r, "Nothing at "+r.URL.Path+".")
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Files embedded in the binary can seek; anything else is copied whole.
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, time.Time{}, rs)
		return
	}
	io.Copy(w, f)
}

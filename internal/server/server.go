// Package server is Pail's one HTTP listener. It routes by the Host header:
// <name>.<base domain>, or a custom hostname, is a pail's site; the base domain itself (or the
// server's bare address) is the installation, which answers the REST API and
// serves the web UI.
package server

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/pails"
)

type Server struct {
	cfg     config.Config
	pails   *pails.Service
	log     *slog.Logger
	version string
	ui      fs.FS
	probe   *prober
	install http.Handler
}

// New builds the listener's handler. ui is the built web UI's files, or nil
// to run without one.
func New(cfg config.Config, svc *pails.Service, ui fs.FS, logger *slog.Logger, version string) *Server {
	s := &Server{cfg: cfg, pails: svc, ui: ui, probe: newProber(), log: logger, version: version}
	s.install = s.installation()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	// The self-check's path is Pail's own on every host, so a check can be
	// answered before anything is known about the name it came in on.
	if strings.HasPrefix(r.URL.Path, probePath) && s.probe.answer(w, r) {
		return
	}
	if s.isInstallation(host) {
		s.install.ServeHTTP(w, r)
		return
	}
	if name, ok := strings.CutSuffix(host, "."+s.cfg.BaseDomain); ok && !strings.Contains(name, ".") {
		s.serveSite(w, r, name)
		return
	}
	if name, ok := s.pails.HostPail(host); ok {
		s.serveSite(w, r, name)
		return
	}
	s.notFound(w, r, "Nothing is hosted at "+host+".")
}

// notFound is the plain page for a host or path nobody claims.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, what string) {
	s.plainPage(w, r, http.StatusNotFound, what)
}

// isInstallation reports whether host addresses Pail itself rather than a
// pail: the base domain, or the server reached by IP or as localhost.
func (s *Server) isInstallation(host string) bool {
	return host == s.cfg.BaseDomain || host == "localhost" || net.ParseIP(host) != nil
}

// hostname is the Host header without its port, lower-cased.
func hostname(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// plainPage is what Pail says when it has no site to serve. It names the
// installation, so it's never mistaken for someone else's site.
func (s *Server) plainPage(w http.ResponseWriter, r *http.Request, status int, what string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		fmt.Fprintf(w, "%s\n\nThis is Pail on %s.\n", what, s.cfg.BaseDomain)
	}
}

// origin is how the caller reaches host: the scheme and port they used for
// this request, on another name.
func origin(r *http.Request, host string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		if !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
			host = net.JoinHostPort(host, port)
		}
	}
	return scheme + "://" + host
}

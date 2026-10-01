// Package server is Pail's one HTTP listener. It routes by the Host header:
// <name>.<base domain> is a pail's site; the base domain itself (or the
// server's bare address) is the installation, which answers the REST API.
package server

import (
	"fmt"
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
	install http.Handler
}

func New(cfg config.Config, svc *pails.Service, logger *slog.Logger, version string) *Server {
	s := &Server{cfg: cfg, pails: svc, log: logger, version: version}
	s.install = s.installation()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	if s.isInstallation(host) {
		s.install.ServeHTTP(w, r)
		return
	}
	if name, ok := strings.CutSuffix(host, "."+s.cfg.BaseDomain); ok && !strings.Contains(name, ".") {
		s.serveSite(w, r, name)
		return
	}
	s.notFound(w, r, "Nothing is hosted at "+host+".")
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

// notFound is the plain page for a host or path nobody claims. It names the
// installation, so it's never mistaken for someone else's site.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, what string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
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

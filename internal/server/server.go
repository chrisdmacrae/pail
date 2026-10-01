// Package server is Pail's one HTTP listener. It routes by the Host header:
// <name>.<base domain>, or a custom hostname, is a pail's site; the base domain itself (or the
// server's bare address) is the installation, which answers the REST API and
// serves the web UI.
package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/githost"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/storage"
)

type Server struct {
	cfg     config.Config
	pails   *pails.Service
	certs   Certs
	git     *githost.Connections
	log     *slog.Logger
	version string
	ui      fs.FS
	probe   *prober
	install http.Handler
	// proxy passes requests on to containers.
	proxy *httputil.ReverseProxy
	// closing is closed as Pail shuts down, so streams that would otherwise
	// stay open for ever end and let it.
	closing     chan struct{}
	closingOnce sync.Once

	// signIns are the OAuth sign-ins under way, by their state.
	signInsMu sync.Mutex
	signIns   map[string]signIn
}

// Close ends the log streams still open. Call it before shutting the
// listeners down.
func (s *Server) Close() { s.closingOnce.Do(func() { close(s.closing) }) }

// Certs is what the server needs from whatever issues its certificates.
type Certs interface {
	// Mode is "internal" or "acme".
	Mode() string
	// RootPEM is the root devices must trust, or nil for a public authority.
	RootPEM() []byte
	// AddHost gets a custom hostname its certificate, or says why it can't.
	AddHost(ctx context.Context, host string) error
	RemoveHost(ctx context.Context, host string)
}

type Options struct {
	Config config.Config
	Pails  *pails.Service
	// UI is the built web UI's files, or nil to run without one.
	UI fs.FS
	// Certs issues certificates, or is nil when Pail serves plain HTTP only.
	Certs Certs
	// Git holds the git hosts Pail is connected to. Nil means none, kept
	// nowhere: connections made then last until the process stops.
	Git     *githost.Connections
	Logger  *slog.Logger
	Version string
}

// New builds the handler for Pail's listener: the HTTPS one, or the only
// one when TLS is off.
func New(o Options) *Server {
	s := &Server{cfg: o.Config, pails: o.Pails, certs: o.Certs, git: o.Git, ui: o.UI, probe: newProber(), log: o.Logger, version: o.Version}
	s.signIns = map[string]signIn{}
	s.closing = make(chan struct{})
	if s.git == nil {
		s.git, _ = githost.LoadConnections(context.Background(), storage.NewMemory(), githost.DefaultClient())
	}
	s.install = s.installation()
	s.proxy = s.newProxy()
	return s
}

// TLSMode is "off", "internal" or "acme".
func (s *Server) TLSMode() string {
	if s.certs == nil {
		return "off"
	}
	return s.certs.Mode()
}

// Plain is what the plain-HTTP listener serves when HTTPS is on: the DNS
// self-check, the root certificate for devices that don't trust it yet, and
// a redirect to HTTPS for everything else.
func (s *Server) Plain() http.Handler {
	suffix := ""
	if _, port, err := net.SplitHostPort(s.cfg.ListenTLS); err == nil && port != "443" {
		suffix = ":" + port
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, probePath) && s.probe.answer(w, r) {
			return
		}
		host := hostname(r.Host)
		if r.URL.Path == "/ca.crt" && s.isInstallation(host) && s.serveRoot(w, r) {
			return
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		http.Redirect(w, r, "https://"+host+suffix+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// serveRoot sends the root certificate of Pail's own authority, if that is
// where its certificates come from.
func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) bool {
	if s.certs == nil || s.certs.RootPEM() == nil {
		return false
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(s.certs.RootPEM())
	return true
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

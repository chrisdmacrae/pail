package server

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/chrisdmacrae/pail/internal/pails"
)

// backend is the container a request is on its way to.
type backend struct {
	pail, container, addr string
}

type backendKey struct{}

// newProxy makes the one reverse proxy every container's requests go
// through. Pail is the only way in to a container's microVM.
func (s *Server) newProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			to := pr.In.Context().Value(backendKey{}).(backend)
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", to.addr
			// The app sees the name and scheme the visitor used.
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     time.Minute,
		},
		// Pass each write on as it comes: event streams depend on it.
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			to := r.Context().Value(backendKey{}).(backend)
			if r.Context().Err() != nil {
				return // the visitor left
			}
			s.log.Warn("container didn't answer", "pail", to.pail, "container", to.container, "err", err)
			s.plainPage(w, r, http.StatusBadGateway, to.container+" in "+to.pail+" didn't answer. See what it printed with pail logs "+to.pail+" --output.")
		},
	}
}

// serveContainer passes a request to one of the live deploy's containers.
// Websockets and other upgrades pass through too.
func (s *Server) serveContainer(w http.ResponseWriter, r *http.Request, live pails.Live, container string) {
	addr, up := live.Backend(container)
	if !up {
		w.Header().Set("Retry-After", "2")
		s.plainPage(w, r, http.StatusServiceUnavailable, container+" in "+live.Pail+" is starting. Try again in a moment.")
		return
	}
	to := backend{pail: live.Pail, container: container, addr: addr}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), backendKey{}, to)))
}

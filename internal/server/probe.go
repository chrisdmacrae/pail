package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// probePath is the one path Pail answers itself on every host: the DNS
// self-check asks for it by a hostname and sees whether the request comes
// back to this same process.
const probePath = "/.well-known/pail/"

// Check is whether a hostname's traffic reaches this Pail.
type Check struct {
	Host       string `json:"host"`
	PointsHere bool   `json:"points_here"`
	// Detail says what was found when it doesn't.
	Detail string `json:"detail,omitempty"`
}

// prober asks for a one-time path at a hostname and recognises its own
// answer. It works wherever Pail runs, since Pail never needs to know its
// outside address: it only needs the request to arrive.
type prober struct {
	mu      sync.Mutex
	pending map[string]string // nonce → the answer only this process knows
	client  *http.Client
	// dialAt, when set, is dialled instead of the hostname's own address.
	dialAt string
}

func newProber() *prober {
	p := &prober{pending: map[string]string{}}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	p.client = &http.Client{
		Timeout: 5 * time.Second,
		// Something else answering with a redirect isn't this Pail.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				switch {
				case p.dialAt != "":
					addr = p.dialAt
				case host == "localhost" || strings.HasSuffix(host, ".localhost"):
					// Every name under localhost is this machine, whether or
					// not the resolver here knows it.
					addr = net.JoinHostPort("127.0.0.1", port)
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	return p
}

func randomID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// check asks base (http://host[:port]) for a fresh probe path.
func (p *prober) check(ctx context.Context, base, host string) Check {
	nonce, answer := randomID(), randomID()
	p.mu.Lock()
	p.pending[nonce] = answer
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, nonce)
		p.mu.Unlock()
	}()

	c := Check{Host: host}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+probePath+nonce, nil)
	if err != nil {
		c.Detail = host + " isn't a name Pail can look up."
		return c
	}
	resp, err := p.client.Do(req)
	if err != nil {
		var dns *net.DNSError
		if errors.As(err, &dns) {
			c.Detail = host + " has no DNS record yet."
		} else {
			c.Detail = host + " resolves, but nothing answered there."
		}
		return c
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK || string(body) != answer {
		c.Detail = host + " resolves, but to something that isn't this Pail."
		return c
	}
	c.PointsHere = true
	return c
}

// answer serves a probe path: the answer for a check in flight, nothing else.
func (p *prober) answer(w http.ResponseWriter, r *http.Request) bool {
	p.mu.Lock()
	answer, ok := p.pending[strings.TrimPrefix(r.URL.Path, probePath)]
	p.mu.Unlock()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, answer)
	return true
}

// DialProbesAt makes the DNS self-check connect to addr whatever a hostname
// resolves to. It is for tests, where no real DNS points at the server.
func (s *Server) DialProbesAt(addr string) { s.probe.dialAt = addr }

// probeBase is where a check of host should connect: plain HTTP, on the port
// this request reached Pail by.
func probeBase(r *http.Request, host string) string {
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" && port != "80" {
			host = net.JoinHostPort(host, port)
		}
	}
	return "http://" + host
}

// CheckBaseDomain is the self-check Pail runs when it starts: does a name
// under the base domain reach this server? It connects on the port Pail
// listens on.
func (s *Server) CheckBaseDomain(ctx context.Context) Check {
	host := "check-" + randomID()[:8] + "." + s.cfg.BaseDomain
	base := "http://" + host
	if _, port, err := net.SplitHostPort(s.cfg.Listen); err == nil && port != "" && port != "80" {
		base = "http://" + net.JoinHostPort(host, port)
	}
	c := s.probe.check(ctx, base, host)
	c.Host = "*." + s.cfg.BaseDomain
	return c
}

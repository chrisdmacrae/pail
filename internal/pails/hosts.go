package pails

import (
	"context"
	"errors"
	"net"
	"regexp"
	"slices"
	"strings"
)

var (
	ErrBadHost      = errors.New("not a hostname")
	ErrHostReserved = errors.New("hostname is under the base domain")
	ErrNoHost       = errors.New("pail has no such hostname")
)

// ErrHostTaken says another pail already answers at a hostname.
type ErrHostTaken struct{ Host, Pail string }

func (e ErrHostTaken) Error() string { return e.Host + " already belongs to " + e.Pail }

// A custom hostname is a full DNS name: two labels or more, ending in a
// letter-led one.
var hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CleanHost takes a hostname as a person might paste it, with a scheme, a
// path or capitals, and returns just the name.
func CleanHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if _, rest, ok := strings.Cut(host, "://"); ok {
		host = rest
	}
	host, _, _ = strings.Cut(host, "/")
	return strings.TrimSuffix(host, ".")
}

// AddHost makes a pail answer at a custom hostname as well as its own. It
// returns the hostname as stored. Adding one the pail already has is fine.
func (s *Service) AddHost(ctx context.Context, name, raw string) (string, error) {
	host := CleanHost(raw)
	if len(host) > 253 || !hostRE.MatchString(host) || net.ParseIP(host) != nil {
		return "", ErrBadHost
	}
	if host == s.baseDomain || strings.HasSuffix(host, "."+s.baseDomain) {
		return "", ErrHostReserved
	}

	// Claim the name first, so two pails can't both be adding it.
	s.mu.Lock()
	e := s.pails[name]
	if e == nil {
		s.mu.Unlock()
		return "", ErrNoPail
	}
	if owner, taken := s.hosts[host]; taken {
		s.mu.Unlock()
		if owner == name {
			return host, nil
		}
		return "", ErrHostTaken{Host: host, Pail: owner}
	}
	s.hosts[host] = name
	s.mu.Unlock()

	err := s.updateRecord(ctx, e, nil, func(r *record) { r.Hosts = append(slices.Clone(r.Hosts), host) })
	if err != nil {
		s.mu.Lock()
		delete(s.hosts, host)
		s.mu.Unlock()
		if errors.Is(err, errRemoved) {
			return "", ErrNoPail
		}
		return "", err
	}
	return host, nil
}

// RemoveHost stops a pail answering at a custom hostname.
func (s *Service) RemoveHost(ctx context.Context, name, raw string) error {
	host := CleanHost(raw)
	s.mu.RLock()
	e := s.pails[name]
	owner := s.hosts[host]
	s.mu.RUnlock()
	if e == nil {
		return ErrNoPail
	}
	if owner != name {
		return ErrNoHost
	}

	err := s.updateRecord(ctx, e, nil, func(r *record) {
		r.Hosts = slices.DeleteFunc(slices.Clone(r.Hosts), func(h string) bool { return h == host })
	})
	if errors.Is(err, errRemoved) {
		return ErrNoPail
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.hosts, host)
	s.mu.Unlock()
	return nil
}

// HostPail returns the pail that answers at a custom hostname.
func (s *Service) HostPail(host string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.hosts[host]
	return name, ok
}

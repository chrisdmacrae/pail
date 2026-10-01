package cli

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// untrusted reports whether a request failed because the installation's
// certificate doesn't chain to anything this machine trusts.
func untrusted(err error) bool {
	var unknown x509.UnknownAuthorityError
	return errors.As(err, &unknown)
}

// fetchRoot asks an installation for the root of its own certificate
// authority, which it sends along in the TLS handshake. Nothing vouches for
// that first copy: like an SSH host key, it is trusted the first time it is
// seen and held to from then on.
func fetchRoot(base string) (rootPEM []byte, fingerprint string, err error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, "", err
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{
		ServerName:         u.Hostname(),
		InsecureSkipVerify: true, // the point is to learn what to verify against
	})
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()

	chain := conn.ConnectionState().PeerCertificates
	root := chain[len(chain)-1]
	if len(chain) < 2 || !root.IsCA || root.CheckSignatureFrom(root) != nil {
		return nil, "", errors.New("it didn't offer a root certificate of its own")
	}
	// The root must actually vouch for what the server presented.
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if _, err := chain[0].Verify(x509.VerifyOptions{Roots: pool, DNSName: u.Hostname()}); err != nil {
		return nil, "", fmt.Errorf("its root certificate doesn't cover %s: %w", u.Hostname(), err)
	}

	sum := sha256.Sum256(root.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), strings.Join(parts, ":"), nil
}

// trust saves an installation's root certificate beside the config file and
// returns the path to put in the profile.
func (a *app) trust(name string, rootPEM []byte) (string, error) {
	path := filepath.Join(filepath.Dir(a.configPath()), name+"-ca.pem")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", usagef("Can't create %s: %v.", a.shortPath(filepath.Dir(path)), err)
	}
	if err := os.WriteFile(path, rootPEM, 0o600); err != nil {
		return "", usagef("Can't write %s: %v.", a.shortPath(path), err)
	}
	return a.shortPath(path), nil
}

// ca prints the installation's root certificate, for adding to a device's
// trust store.
func (a *app) ca(args []string) error {
	if len(args) != 0 {
		return usagef("pail ca takes no arguments.")
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	var info apiInfo
	if err := c.get("/api/v1/info", &info); err != nil {
		return err
	}
	if info.TLS != "internal" {
		reason := "its certificates come from Let's Encrypt, which devices already trust"
		if info.TLS == "off" {
			reason = "it serves plain HTTP"
		}
		return &exitError{code: ExitNotFound, msg: fmt.Sprintf("%s has no root certificate to hand out: %s.", c.target.URL, reason)}
	}
	resp, err := c.do("GET", "/ca.crt", nil, 0, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(a.env.Stdout, resp.Body)
	return err
}

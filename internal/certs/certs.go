// Package certs gets Pail its TLS certificates. There are two ways, and the
// server's configuration picks one:
//
//   - Pail's own certificate authority, the default: a root made on first
//     start, constrained to the base domain, that devices trust once.
//   - Let's Encrypt, by DNS-01, when a DNS provider and token are set: a
//     wildcard for the base domain and a certificate for each custom hostname.
package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/storage"
)

// ErrNeedsACME is returned for a custom hostname when certificates come from
// Pail's own authority, which can only sign for the base domain.
var ErrNeedsACME = errors.New("custom hostnames need Let's Encrypt mode")

// Manager hands the TLS listener its certificates and keeps them fresh.
type Manager interface {
	// Mode is "internal" or "acme".
	Mode() string
	// GetCertificate picks the certificate for a connection, by its SNI name.
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
	// RootPEM is the root certificate devices must trust, or nil when the
	// certificates chain to a public authority.
	RootPEM() []byte
	// AddHost gets a certificate for a custom hostname. It returns once the
	// certificate is ready to serve, or says why there can't be one.
	AddHost(ctx context.Context, host string) error
	// RemoveHost forgets a custom hostname's certificate.
	RemoveHost(ctx context.Context, host string)
	// Run renews certificates until ctx ends.
	Run(ctx context.Context)
}

type Options struct {
	Store      storage.Store
	BaseDomain string
	ACME       config.ACME
	// Hosts are the custom hostnames pails already have.
	Hosts  []string
	Logger *slog.Logger
}

// New sets up the mode the configuration asks for. In Let's Encrypt mode it
// returns once the base domain's wildcard is in hand, which on a first start
// means waiting for DNS.
func New(ctx context.Context, o Options) (Manager, error) {
	if o.ACME.Enabled() {
		return newACME(ctx, o)
	}
	return newInternal(ctx, o)
}

// fresh reports whether a certificate has more than a third of its life
// left. Past that, it's time for a new one.
func fresh(leaf *x509.Certificate, now time.Time) bool {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return now.Before(leaf.NotAfter.Add(-life / 3))
}

// parsePair reads a certificate chain and its key from one PEM bundle.
func parsePair(bundle []byte) (*tls.Certificate, error) {
	var certPEM, keyPEM []byte
	for rest := bundle; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		} else {
			keyPEM = append(keyPEM, pem.EncodeToMemory(block)...)
		}
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
		return nil, err
	}
	return &cert, nil
}

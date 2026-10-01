package certs

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/storage"
)

const (
	rootKey  = "tls/internal/root.pem"
	rootLife = 10 * 365 * 24 * time.Hour
	leafLife = 7 * 24 * time.Hour
)

// internalCA is Pail's own certificate authority. Its root can sign only
// for the base domain, so trusting it lets it speak for nothing else.
type internalCA struct {
	base    string
	root    *tls.Certificate
	rootPEM []byte

	mu   sync.Mutex
	leaf *tls.Certificate
}

func newInternal(ctx context.Context, o Options) (*internalCA, error) {
	ca := &internalCA{base: o.BaseDomain}

	bundle, err := o.Store.Read(ctx, rootKey)
	switch {
	case err == nil:
		if ca.root, err = parsePair(bundle); err != nil {
			return nil, fmt.Errorf("the stored root certificate can't be read: %w", err)
		}
		// A root made for another base domain can't sign for this one.
		if got := ca.root.Leaf.PermittedDNSDomains; len(got) != 1 || got[0] != o.BaseDomain {
			o.Logger.Warn("the base domain changed, so pail is making a new root certificate; devices need to trust it again", "base_domain", o.BaseDomain)
			ca.root = nil
		}
	case !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}

	if ca.root == nil {
		if bundle, err = newRoot(o.BaseDomain); err != nil {
			return nil, err
		}
		if err := o.Store.Put(ctx, rootKey, bytes.NewReader(bundle), int64(len(bundle)), "application/x-pem-file"); err != nil {
			return nil, err
		}
		if ca.root, err = parsePair(bundle); err != nil {
			return nil, err
		}
		o.Logger.Info("made this pail's root certificate; each device trusts it once", "base_domain", o.BaseDomain)
	}
	ca.rootPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.root.Certificate[0]})

	if _, err := ca.current(); err != nil {
		return nil, err
	}
	return ca, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// newRoot makes the root certificate and key, as one PEM bundle.
func newRoot(base string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{Organization: []string{"Pail"}, CommonName: "Pail on " + base},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(rootLife),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		// The constraint is critical: software that doesn't understand it
		// must reject the root rather than trust it for everything.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{base},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...,
	), nil
}

// current returns the wildcard for the base domain, issuing a new one when
// the last is past its half-life. Nobody ever handles these: they last a
// week and exist only in memory.
func (ca *internalCA) current() (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	now := time.Now()
	if ca.leaf != nil && now.Before(ca.leaf.Leaf.NotBefore.Add(leafLife/2)) {
		return ca.leaf, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: ca.base},
		DNSNames:     []string{ca.base, "*." + ca.base},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(leafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.root.Leaf, &key.PublicKey, ca.root.PrivateKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	// The root rides along in the handshake so pail login can pick it up.
	ca.leaf = &tls.Certificate{Certificate: [][]byte{der, ca.root.Certificate[0]}, PrivateKey: key, Leaf: leaf}
	return ca.leaf, nil
}

func (ca *internalCA) Mode() string    { return "internal" }
func (ca *internalCA) RootPEM() []byte { return ca.rootPEM }

func (ca *internalCA) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return ca.current()
}

func (ca *internalCA) AddHost(context.Context, string) error { return ErrNeedsACME }
func (ca *internalCA) RemoveHost(context.Context, string)    {}

// Run has nothing to do: current renews as connections arrive.
func (ca *internalCA) Run(ctx context.Context) { <-ctx.Done() }

package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/storage"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func rootPool(t *testing.T, m Manager) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.RootPEM()) {
		t.Fatal("RootPEM isn't a certificate")
	}
	return pool
}

func TestInternalCA(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	m, err := New(ctx, Options{Store: store, BaseDomain: "pail.lan", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode() != "internal" {
		t.Fatalf("mode %q", m.Mode())
	}
	cert, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := rootPool(t, m)

	for _, name := range []string{"pail.lan", "blog.pail.lan"} {
		if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.com"}); err == nil {
		t.Error("the wildcard verified for example.com")
	}
	if life := cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore); life > 8*24*time.Hour {
		t.Errorf("leaf lasts %v, want about a week", life)
	}
	// The root comes along in the handshake, for pail login to pick up.
	if len(cert.Certificate) != 2 {
		t.Errorf("chain has %d certificates, want leaf and root", len(cert.Certificate))
	}

	// A stolen key can't speak for any other site: the root is constrained
	// to the base domain, and says so critically.
	ca := m.(*internalCA)
	root := ca.root.Leaf
	if !root.PermittedDNSDomainsCritical || len(root.PermittedDNSDomains) != 1 || root.PermittedDNSDomains[0] != "pail.lan" {
		t.Errorf("root constraints: critical=%v domains=%v", root.PermittedDNSDomainsCritical, root.PermittedDNSDomains)
	}
	if years := root.NotAfter.Sub(root.NotBefore).Hours() / 24 / 365; years < 9.9 || years > 10.1 {
		t.Errorf("root lasts %.1f years, want 10", years)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	evil := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "bank.example"}, DNSNames: []string{"bank.example"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, evil, root, &key.PublicKey, ca.root.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := x509.ParseCertificate(der)
	if _, err := forged.Verify(x509.VerifyOptions{Roots: pool, DNSName: "bank.example"}); err == nil {
		t.Error("a certificate for bank.example, signed with Pail's root key, verified")
	}

	// The root is kept: a restart hands out the same one.
	again, err := New(ctx, Options{Store: store, BaseDomain: "pail.lan", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if string(again.RootPEM()) != string(m.RootPEM()) {
		t.Error("a restart made a new root")
	}
	// Its key is stored as PKCS#8, beside the certificate.
	bundle, _ := store.Read(ctx, rootKey)
	block, rest := pem.Decode(bundle)
	keyBlock, _ := pem.Decode(rest)
	if block == nil || keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Error("stored root isn't a certificate followed by its key")
	}

	// A new base domain needs a new root: the old one can't sign for it.
	moved, err := New(ctx, Options{Store: store, BaseDomain: "home.arpa", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if string(moved.RootPEM()) == string(m.RootPEM()) {
		t.Error("the root wasn't replaced when the base domain changed")
	}

	if err := m.AddHost(ctx, "blog.home.example"); err != ErrNeedsACME {
		t.Errorf("AddHost on the internal CA: %v", err)
	}
}

func TestDNSProviders(t *testing.T) {
	if _, err := dnsProvider("nope", "t"); err == nil || !strings.Contains(err.Error(), "cloudflare") {
		t.Errorf("unknown provider: %v", err)
	}
	for _, name := range ProviderNames() {
		if _, err := dnsProvider(name, "a-token"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if strings.Contains(strings.Join(ProviderNames(), " "), "challtestsrv") {
		t.Error("the test provider is listed as one to use")
	}

	// Let's Encrypt mode is chosen by configuration, and an unknown provider
	// stops Pail before it talks to anyone.
	_, err := New(context.Background(), Options{
		Store: storage.NewMemory(), BaseDomain: "pail.example", Logger: quiet,
		ACME: config.ACME{DNSProvider: "nope", DNSToken: "t"},
	})
	if err == nil || !strings.Contains(err.Error(), "isn't one Pail knows") {
		t.Errorf("New with an unknown provider: %v", err)
	}
}

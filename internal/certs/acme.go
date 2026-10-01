package certs

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/chrisdmacrae/pail/internal/storage"
)

// LetsEncrypt is the ACME directory Pail uses unless PAIL_ACME_DIRECTORY
// names another, such as Let's Encrypt's staging one.
const LetsEncrypt = lego.LEDirectoryProduction

// wildcard names the certificate for the base domain and everything under
// it, in the manager's map and in storage.
const wildcard = "_wildcard"

// How often Run looks for certificates due for renewal.
const renewEvery = 12 * time.Hour

// acmeManager gets certificates from Let's Encrypt, proving ownership of
// each name by publishing a DNS record through the provider's API. That
// works on a server with private addresses and no open ports.
type acmeManager struct {
	store  storage.Store
	base   string
	prefix string // where this directory's account and certificates are kept
	client *lego.Client
	log    *slog.Logger

	// obtain lets one certificate be requested at a time.
	obtain sync.Mutex

	mu    sync.RWMutex
	certs map[string]*tls.Certificate
	hosts map[string]bool // custom hostnames to keep renewed
}

// account is the ACME account Pail registers once and keeps.
type account struct {
	Email        string                 `json:"email"`
	KeyPEM       string                 `json:"key"`
	Registration *registration.Resource `json:"registration"`
	key          crypto.PrivateKey
}

func (a *account) GetEmail() string                        { return a.Email }
func (a *account) GetRegistration() *registration.Resource { return a.Registration }
func (a *account) GetPrivateKey() crypto.PrivateKey        { return a.key }

func newACME(ctx context.Context, o Options) (*acmeManager, error) {
	directory := o.ACME.Directory
	if directory == "" {
		directory = LetsEncrypt
	}
	// Each directory gets its own account and certificates, so trying the
	// staging one never mixes with the real thing.
	sum := sha256.Sum256([]byte(directory))
	m := &acmeManager{
		store:  o.Store,
		base:   o.BaseDomain,
		prefix: "tls/acme/" + hex.EncodeToString(sum[:4]) + "/",
		log:    o.Logger,
		certs:  map[string]*tls.Certificate{},
		hosts:  map[string]bool{},
	}
	for _, host := range o.Hosts {
		m.hosts[host] = true
	}

	provider, err := dnsProvider(o.ACME.DNSProvider, o.ACME.DNSToken)
	if err != nil {
		return nil, err
	}
	acct, err := m.loadAccount(ctx, o.ACME.Email)
	if err != nil {
		return nil, err
	}
	cfg := lego.NewConfig(acct)
	cfg.CADirURL = directory
	cfg.Certificate.KeyType = certcrypto.EC256
	if m.client, err = lego.NewClient(cfg); err != nil {
		return nil, fmt.Errorf("can't reach the certificate authority at %s: %w", directory, err)
	}

	_, testing := provider.(challTestSrv)
	err = m.client.Challenge.SetDNS01Provider(provider,
		dns01.CondOption(len(o.ACME.Resolvers) > 0, dns01.AddRecursiveNameservers(dns01.ParseNameservers(o.ACME.Resolvers))),
		// The test DNS server has no zones to look the record up in.
		dns01.CondOption(testing, dns01.WrapPreCheck(func(string, string, string, dns01.PreCheckFunc) (bool, error) { return true, nil })),
	)
	if err != nil {
		return nil, err
	}

	if acct.Registration == nil {
		reg, err := m.client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("can't register with the certificate authority: %w", err)
		}
		acct.Registration = reg
		if err := m.saveAccount(ctx, acct); err != nil {
			return nil, err
		}
		o.Logger.Info("registered with the certificate authority", "directory", directory)
	}

	// Pail can't serve HTTPS without this one, so a first start waits for it
	// and a failure here is a failure to start.
	if err := m.ensure(ctx, wildcard); err != nil {
		return nil, fmt.Errorf("can't get a certificate for %s and *.%s: %w", m.base, m.base, err)
	}
	return m, nil
}

func (m *acmeManager) loadAccount(ctx context.Context, email string) (*account, error) {
	acct := &account{}
	b, err := m.store.Read(ctx, m.prefix+"account.json")
	switch {
	case err == nil:
		if err := json.Unmarshal(b, acct); err != nil {
			return nil, fmt.Errorf("the stored ACME account can't be read: %w", err)
		}
		block, _ := pem.Decode([]byte(acct.KeyPEM))
		if block == nil {
			return nil, errors.New("the stored ACME account has no key")
		}
		if acct.key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
			return nil, err
		}
		return acct, nil
	case !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	acct.Email, acct.key = email, key
	acct.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return acct, nil
}

func (m *acmeManager) saveAccount(ctx context.Context, acct *account) error {
	b, err := json.Marshal(acct)
	if err != nil {
		return err
	}
	return m.store.Put(ctx, m.prefix+"account.json", bytes.NewReader(b), int64(len(b)), "application/json")
}

func (m *acmeManager) certKey(name string) string { return m.prefix + "certs/" + name + ".pem" }

func (m *acmeManager) domains(name string) []string {
	if name == wildcard {
		return []string{m.base, "*." + m.base}
	}
	return []string{name}
}

// ensure makes sure name has a certificate with life left in it: the one in
// memory, else the one in storage, else a new one from the authority.
func (m *acmeManager) ensure(ctx context.Context, name string) error {
	m.obtain.Lock()
	defer m.obtain.Unlock()

	m.mu.RLock()
	have := m.certs[name]
	m.mu.RUnlock()
	if have == nil {
		if bundle, err := m.store.Read(ctx, m.certKey(name)); err == nil {
			if cert, err := parsePair(bundle); err == nil {
				have = cert
				m.set(name, cert)
			}
		} else if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	if have != nil && fresh(have.Leaf, time.Now()) {
		return nil
	}

	domains := m.domains(name)
	m.log.Info("asking for a certificate; this waits for DNS", "names", strings.Join(domains, ", "))
	res, err := m.client.Certificate.Obtain(certificate.ObtainRequest{Domains: domains, Bundle: true})
	if err != nil {
		// An old certificate that still works keeps serving.
		if have != nil && time.Now().Before(have.Leaf.NotAfter) {
			m.log.Error("couldn't renew a certificate; the current one keeps serving", "names", strings.Join(domains, ", "), "expires", have.Leaf.NotAfter, "err", err)
			return nil
		}
		return err
	}
	bundle := append(append([]byte{}, res.Certificate...), res.PrivateKey...)
	cert, err := parsePair(bundle)
	if err != nil {
		return err
	}
	if err := m.store.Put(ctx, m.certKey(name), bytes.NewReader(bundle), int64(len(bundle)), "application/x-pem-file"); err != nil {
		return err
	}
	m.set(name, cert)
	m.log.Info("got a certificate", "names", strings.Join(domains, ", "), "expires", cert.Leaf.NotAfter)
	return nil
}

func (m *acmeManager) set(name string, cert *tls.Certificate) {
	m.mu.Lock()
	m.certs[name] = cert
	m.mu.Unlock()
}

func (m *acmeManager) Mode() string    { return "acme" }
func (m *acmeManager) RootPEM() []byte { return nil }

// GetCertificate serves a custom hostname its own certificate, and anything
// else the base domain's wildcard.
func (m *acmeManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if cert := m.certs[strings.ToLower(hello.ServerName)]; cert != nil {
		return cert, nil
	}
	return m.certs[wildcard], nil
}

func (m *acmeManager) AddHost(ctx context.Context, host string) error {
	if err := m.ensure(ctx, host); err != nil {
		return err
	}
	m.mu.Lock()
	m.hosts[host] = true
	m.mu.Unlock()
	return nil
}

func (m *acmeManager) RemoveHost(ctx context.Context, host string) {
	m.mu.Lock()
	delete(m.hosts, host)
	delete(m.certs, host)
	m.mu.Unlock()
	if err := m.store.DeletePrefix(ctx, m.certKey(host)); err != nil {
		m.log.Error("couldn't delete a certificate", "host", host, "err", err)
	}
}

// Run gets certificates for the custom hostnames pails already have, then
// renews everything as it comes due.
func (m *acmeManager) Run(ctx context.Context) {
	for {
		m.mu.RLock()
		names := []string{wildcard}
		for host := range m.hosts {
			names = append(names, host)
		}
		m.mu.RUnlock()
		for _, name := range names {
			if err := m.ensure(ctx, name); err != nil {
				m.log.Error("couldn't get a certificate", "names", strings.Join(m.domains(name), ", "), "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(renewEvery):
		}
	}
}

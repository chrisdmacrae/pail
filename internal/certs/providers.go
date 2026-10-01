package certs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/providers/dns/bunny"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/desec"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/providers/dns/gandiv5"
	"github.com/go-acme/lego/v4/providers/dns/hetzner"
	"github.com/go-acme/lego/v4/providers/dns/netlify"
	"github.com/go-acme/lego/v4/providers/dns/njalla"
)

// providers are the DNS hosts Pail can prove ownership through: the ones
// whose API takes a single token, which is all Pail's configuration carries.
var providers = map[string]func(token string) (challenge.Provider, error){
	"bunny": func(token string) (challenge.Provider, error) {
		c := bunny.NewDefaultConfig()
		c.APIKey = token
		return bunny.NewDNSProviderConfig(c)
	},
	"cloudflare": func(token string) (challenge.Provider, error) {
		c := cloudflare.NewDefaultConfig()
		c.AuthToken = token
		return cloudflare.NewDNSProviderConfig(c)
	},
	"desec": func(token string) (challenge.Provider, error) {
		c := desec.NewDefaultConfig()
		c.Token = token
		return desec.NewDNSProviderConfig(c)
	},
	"digitalocean": func(token string) (challenge.Provider, error) {
		c := digitalocean.NewDefaultConfig()
		c.AuthToken = token
		return digitalocean.NewDNSProviderConfig(c)
	},
	"duckdns": func(token string) (challenge.Provider, error) {
		c := duckdns.NewDefaultConfig()
		c.Token = token
		return duckdns.NewDNSProviderConfig(c)
	},
	"gandi": func(token string) (challenge.Provider, error) {
		c := gandiv5.NewDefaultConfig()
		c.PersonalAccessToken = token
		return gandiv5.NewDNSProviderConfig(c)
	},
	"hetzner": func(token string) (challenge.Provider, error) {
		c := hetzner.NewDefaultConfig()
		c.APIToken = token
		return hetzner.NewDNSProviderConfig(c)
	},
	"netlify": func(token string) (challenge.Provider, error) {
		c := netlify.NewDefaultConfig()
		c.Token = token
		return netlify.NewDNSProviderConfig(c)
	},
	"njalla": func(token string) (challenge.Provider, error) {
		c := njalla.NewDefaultConfig()
		c.Token = token
		return njalla.NewDNSProviderConfig(c)
	},
	// For testing against Pebble, Let's Encrypt's test server. The "token"
	// is the address of pebble-challtestsrv's management API.
	"challtestsrv": func(url string) (challenge.Provider, error) {
		return challTestSrv{url: strings.TrimRight(url, "/")}, nil
	},
}

// ProviderNames lists what PAIL_ACME_DNS_PROVIDER accepts.
func ProviderNames() []string {
	var names []string
	for name := range providers {
		if name != "challtestsrv" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func dnsProvider(name, token string) (challenge.Provider, error) {
	make, ok := providers[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("PAIL_ACME_DNS_PROVIDER %q isn't one Pail knows. Use one of: %s", name, strings.Join(ProviderNames(), ", "))
	}
	p, err := make(token)
	if err != nil {
		return nil, fmt.Errorf("PAIL_ACME_DNS_TOKEN doesn't work for %s: %w", name, err)
	}
	return p, nil
}

// challTestSrv publishes TXT records through pebble-challtestsrv.
type challTestSrv struct{ url string }

func (c challTestSrv) post(path string, body map[string]string) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(c.url+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv answered %s", resp.Status)
	}
	return nil
}

func (c challTestSrv) Present(domain, _, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)
	return c.post("/set-txt", map[string]string{"host": fqdn, "value": value})
}

func (c challTestSrv) CleanUp(domain, _, keyAuth string) error {
	fqdn, _ := dns01.GetRecord(domain, keyAuth)
	return c.post("/clear-txt", map[string]string{"host": fqdn})
}

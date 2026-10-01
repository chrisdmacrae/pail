// Package config reads Pail's server configuration. Pail is configured
// entirely by environment variables; there is no config file.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	// Token is the installation's secret access token (PAIL_TOKEN).
	Token string
	// BaseDomain is the domain every pail gets a name under (PAIL_BASE_DOMAIN).
	BaseDomain string
	// MaxUploadSize is the largest archive accepted, in bytes (PAIL_MAX_UPLOAD_SIZE).
	MaxUploadSize int64
	// MaxDeploys is how many good deploys are kept per pail for rollback
	// (PAIL_MAX_DEPLOYS). Failed ones are counted apart, to the same limit.
	MaxDeploys int
	// MaxFunctionMemory is the most memory one function copy may request,
	// in bytes (PAIL_MAX_FUNCTION_MEMORY).
	MaxFunctionMemory int64
	// Listen is the address Pail's listener binds (PAIL_LISTEN).
	Listen string

	S3   S3
	ACME ACME
}

// ACME turns on Let's Encrypt certificates by DNS-01, and with them custom
// hostnames.
type ACME struct {
	DNSProvider string // PAIL_ACME_DNS_PROVIDER
	DNSToken    string // PAIL_ACME_DNS_TOKEN
	Email       string // PAIL_ACME_EMAIL, optional
}

// Enabled reports whether Let's Encrypt mode is on.
func (a ACME) Enabled() bool { return a.DNSProvider != "" && a.DNSToken != "" }

// S3 points Pail at versitygw (or any S3-compatible gateway).
type S3 struct {
	Endpoint  string // PAIL_S3_ENDPOINT, e.g. http://versitygw:7070
	AccessKey string // PAIL_S3_ACCESS_KEY
	SecretKey string // PAIL_S3_SECRET_KEY
	Bucket    string // PAIL_S3_BUCKET
	Region    string // PAIL_S3_REGION
}

// Load builds a Config from getenv (normally os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Token:      strings.TrimSpace(getenv("PAIL_TOKEN")),
		BaseDomain: strings.ToLower(strings.Trim(get("PAIL_BASE_DOMAIN", "pail.lan"), ".")),
		Listen:     get("PAIL_LISTEN", ":80"),
		S3: S3{
			Endpoint:  get("PAIL_S3_ENDPOINT", ""),
			AccessKey: get("PAIL_S3_ACCESS_KEY", ""),
			SecretKey: get("PAIL_S3_SECRET_KEY", ""),
			Bucket:    get("PAIL_S3_BUCKET", "pail"),
			Region:    get("PAIL_S3_REGION", "us-east-1"),
		},
	}
	if c.Token == "" {
		return c, errors.New("PAIL_TOKEN is not set. Set it to a long random secret; every client sends it with each request")
	}

	var err error
	if c.MaxUploadSize, err = ParseSize(get("PAIL_MAX_UPLOAD_SIZE", "100MB")); err != nil {
		return c, fmt.Errorf("PAIL_MAX_UPLOAD_SIZE: %w", err)
	}
	if c.MaxFunctionMemory, err = ParseSize(get("PAIL_MAX_FUNCTION_MEMORY", "1GB")); err != nil {
		return c, fmt.Errorf("PAIL_MAX_FUNCTION_MEMORY: %w", err)
	}
	if c.MaxDeploys, err = strconv.Atoi(get("PAIL_MAX_DEPLOYS", "10")); err != nil || c.MaxDeploys < 1 {
		return c, errors.New("PAIL_MAX_DEPLOYS: use a whole number, 1 or more")
	}

	c.ACME = ACME{
		DNSProvider: get("PAIL_ACME_DNS_PROVIDER", ""),
		DNSToken:    get("PAIL_ACME_DNS_TOKEN", ""),
		Email:       get("PAIL_ACME_EMAIL", ""),
	}
	if (c.ACME.DNSProvider == "") != (c.ACME.DNSToken == "") {
		return c, errors.New("PAIL_ACME_DNS_PROVIDER and PAIL_ACME_DNS_TOKEN go together. Set both, or neither")
	}

	if c.S3.Endpoint == "" || c.S3.AccessKey == "" || c.S3.SecretKey == "" {
		return c, errors.New("PAIL_S3_ENDPOINT, PAIL_S3_ACCESS_KEY and PAIL_S3_SECRET_KEY must point at versitygw")
	}
	return c, nil
}

var sizeUnits = []struct {
	suffix string
	bytes  int64
}{
	{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40},
	{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
	{"B", 1},
}

// ParseSize reads sizes like "100MB", "1GB" or a plain byte count.
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.bytes
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, errors.New(`use a size like "100MB" or "1GB"`)
	}
	return int64(n * float64(mult)), nil
}

// FormatSize prints a byte count the way ParseSize reads it.
func FormatSize(n int64) string {
	for _, u := range []struct {
		suffix string
		bytes  int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}} {
		if n >= u.bytes {
			return strings.TrimSuffix(strconv.FormatFloat(float64(n)/float64(u.bytes), 'f', 1, 64), ".0") + u.suffix
		}
	}
	return strconv.FormatInt(n, 10) + "B"
}

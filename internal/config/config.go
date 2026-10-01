// Package config reads Pail's server configuration. Pail is configured
// entirely by environment variables; there is no config file.
package config

import (
	"errors"
	"fmt"
	"regexp"
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
	// MaxContainerMemory is the most memory one container may request, in
	// bytes (PAIL_MAX_CONTAINER_MEMORY).
	MaxContainerMemory int64
	// AllowLAN names the pails whose containers may reach the home network
	// (PAIL_ALLOW_LAN, comma-separated). Every other pail's can't.
	AllowLAN []string
	// SecretsKey is what pails' secrets are sealed with (PAIL_SECRETS_KEY):
	// any text, long and random for preference. Unset, Pail makes a key of
	// its own and keeps it in DataDir.
	SecretsKey string
	// Listen is the address the plain-HTTP listener binds (PAIL_LISTEN).
	Listen string
	// ListenTLS is the address the HTTPS listener binds (PAIL_LISTEN_TLS).
	ListenTLS string
	// DataDir is local disk for what builds, containers and functions need:
	// root filesystems and work disks (PAIL_DATA_DIR).
	DataDir string
	// Firecracker is the firecracker binary (PAIL_FIRECRACKER), and Kernel
	// the guest kernel microVMs boot (PAIL_KERNEL).
	Firecracker string
	Kernel      string
	// Runtime is what builds, containers and functions run in
	// (PAIL_RUNTIME): "firecracker" for microVMs, "container" for the
	// containers of Docker or Podman, or "auto" for microVMs where this
	// machine can run them and containers where it can't.
	Runtime string
	// ContainerSocket is the container engine's API socket
	// (PAIL_CONTAINER_SOCKET), and ContainerNetwork the engine's network
	// Pail's containers join (PAIL_CONTAINER_NETWORK).
	ContainerSocket  string
	ContainerNetwork string
	// TLSOff turns HTTPS off (PAIL_TLS=off): Pail then serves everything
	// over plain HTTP, for local development or behind a proxy that
	// terminates TLS itself.
	TLSOff bool

	S3   S3
	ACME ACME
	// OAuth holds the OAuth app for each git host that has one, keyed by
	// the host: "github", "gitlab", "bitbucket", "gitea", "forgejo".
	OAuth map[string]OAuthApp
}

// OAuthApp is an OAuth app registered with a git host
// (PAIL_OAUTH_<HOST>_CLIENT_ID and _CLIENT_SECRET), so people can sign in
// instead of pasting a token.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	// Server is where the app is registered, for a host you run yourself
	// (PAIL_OAUTH_<HOST>_SERVER).
	Server string
}

// oauthHosts are the hosts an app can be set up for, and whether one needs
// to be told its server.
var oauthHosts = []struct {
	name       string
	needServer bool
}{{"github", false}, {"gitlab", false}, {"bitbucket", false}, {"gitea", true}, {"forgejo", true}}

// ACME turns on Let's Encrypt certificates by DNS-01, and with them custom
// hostnames.
type ACME struct {
	DNSProvider string // PAIL_ACME_DNS_PROVIDER
	DNSToken    string // PAIL_ACME_DNS_TOKEN
	Email       string // PAIL_ACME_EMAIL, optional
	// Directory is the ACME directory to use instead of Let's Encrypt's
	// production one (PAIL_ACME_DIRECTORY), such as its staging one.
	Directory string
	// Resolvers are DNS servers to check the challenge record with
	// (PAIL_ACME_RESOLVERS), for when the home resolver answers for the
	// domain itself and would never see the public record.
	Resolvers []string
}

// Enabled reports whether Let's Encrypt mode is on.
func (a ACME) Enabled() bool { return a.DNSProvider != "" && a.DNSToken != "" }

// S3 points Pail at where it keeps everything: versitygw beside it, or any
// other store that speaks S3.
type S3 struct {
	Endpoint  string // PAIL_S3_ENDPOINT, e.g. http://versitygw:7070
	AccessKey string // PAIL_S3_ACCESS_KEY
	SecretKey string // PAIL_S3_SECRET_KEY
	Bucket    string // PAIL_S3_BUCKET
	Region    string // PAIL_S3_REGION
	// Addressing is where the bucket's name goes in a request, from
	// PAIL_S3_ADDRESSING: "path" for after the host, "virtual" for in front
	// of it, or "auto" for whichever the store is known to want.
	Addressing string
}

// Load builds a Config from getenv (normally os.Getenv).
// pailNameRE is what a pail's name looks like, as the pails package has it.
var pailNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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
		ListenTLS:  get("PAIL_LISTEN_TLS", ":443"),
		DataDir:    get("PAIL_DATA_DIR", "/var/lib/pail"),
		SecretsKey: strings.TrimSpace(getenv("PAIL_SECRETS_KEY")),
		S3: S3{
			Endpoint:  get("PAIL_S3_ENDPOINT", ""),
			AccessKey: get("PAIL_S3_ACCESS_KEY", ""),
			SecretKey: get("PAIL_S3_SECRET_KEY", ""),
			Bucket:    get("PAIL_S3_BUCKET", "pail"),
			Region:    get("PAIL_S3_REGION", "us-east-1"),

			Addressing: strings.ToLower(get("PAIL_S3_ADDRESSING", "auto")),
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
	if c.MaxContainerMemory, err = ParseSize(get("PAIL_MAX_CONTAINER_MEMORY", "2GB")); err != nil {
		return c, fmt.Errorf("PAIL_MAX_CONTAINER_MEMORY: %w", err)
	}
	for _, name := range strings.Split(getenv("PAIL_ALLOW_LAN"), ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name == "" {
			continue
		}
		if !pailNameRE.MatchString(name) {
			return c, fmt.Errorf("PAIL_ALLOW_LAN: list pails by name, with commas between them, like media,backups. Got %q", name)
		}
		c.AllowLAN = append(c.AllowLAN, name)
	}
	if c.MaxDeploys, err = strconv.Atoi(get("PAIL_MAX_DEPLOYS", "10")); err != nil || c.MaxDeploys < 1 {
		return c, errors.New("PAIL_MAX_DEPLOYS: use a whole number, 1 or more")
	}

	c.ACME = ACME{
		DNSProvider: get("PAIL_ACME_DNS_PROVIDER", ""),
		DNSToken:    get("PAIL_ACME_DNS_TOKEN", ""),
		Email:       get("PAIL_ACME_EMAIL", ""),
		Directory:   get("PAIL_ACME_DIRECTORY", ""),
	}
	if r := get("PAIL_ACME_RESOLVERS", ""); r != "" {
		c.ACME.Resolvers = strings.Split(strings.ReplaceAll(r, " ", ""), ",")
	}
	switch strings.ToLower(get("PAIL_TLS", "on")) {
	case "on":
	case "off":
		c.TLSOff = true
	default:
		return c, errors.New(`PAIL_TLS: use "off" to serve plain HTTP only, or leave it unset`)
	}
	if (c.ACME.DNSProvider == "") != (c.ACME.DNSToken == "") {
		return c, errors.New("PAIL_ACME_DNS_PROVIDER and PAIL_ACME_DNS_TOKEN go together. Set both, or neither")
	}

	c.Firecracker = get("PAIL_FIRECRACKER", "firecracker")
	c.Kernel = get("PAIL_KERNEL", c.DataDir+"/vmlinux")
	switch runtime := strings.ToLower(get("PAIL_RUNTIME", "auto")); runtime {
	case "auto", "firecracker", "container":
		c.Runtime = runtime
	case "docker", "podman":
		c.Runtime = "container"
	default:
		return c, errors.New(`PAIL_RUNTIME: use "firecracker" for microVMs, "container" for Docker or Podman, or leave it unset for whichever this machine can run`)
	}
	c.ContainerSocket = get("PAIL_CONTAINER_SOCKET", "/var/run/docker.sock")
	c.ContainerNetwork = get("PAIL_CONTAINER_NETWORK", "pail")

	c.OAuth = map[string]OAuthApp{}
	for _, h := range oauthHosts {
		prefix := "PAIL_OAUTH_" + strings.ToUpper(h.name) + "_"
		app := OAuthApp{
			ClientID:     get(prefix+"CLIENT_ID", ""),
			ClientSecret: get(prefix+"CLIENT_SECRET", ""),
			Server:       strings.TrimRight(get(prefix+"SERVER", ""), "/"),
		}
		switch {
		case app.ClientID == "" && app.ClientSecret == "":
			continue
		case app.ClientID == "" || app.ClientSecret == "":
			return c, fmt.Errorf("%sCLIENT_ID and %sCLIENT_SECRET go together. Set both, or neither", prefix, prefix)
		case h.needServer && app.Server == "":
			return c, fmt.Errorf("%sSERVER must say where your %s lives, like https://git.home.example", prefix, h.name)
		}
		c.OAuth[h.name] = app
	}

	if c.S3.Endpoint == "" || c.S3.AccessKey == "" || c.S3.SecretKey == "" {
		return c, errors.New("PAIL_S3_ENDPOINT, PAIL_S3_ACCESS_KEY and PAIL_S3_SECRET_KEY must point at Pail's storage: versitygw, or another store that speaks S3")
	}
	switch c.S3.Addressing {
	case "auto", "path", "virtual":
	default:
		return c, fmt.Errorf("PAIL_S3_ADDRESSING is %q. It is \"auto\", \"path\" or \"virtual\"", c.S3.Addressing)
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

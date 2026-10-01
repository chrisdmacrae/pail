package config

import (
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

var s3 = map[string]string{
	"PAIL_S3_ENDPOINT":   "http://versitygw:7070",
	"PAIL_S3_ACCESS_KEY": "pail",
	"PAIL_S3_SECRET_KEY": "secret",
}

func TestLoadRefusesToStartWithoutAToken(t *testing.T) {
	if _, err := Load(env(s3)); err == nil || !strings.Contains(err.Error(), "PAIL_TOKEN") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	vars := map[string]string{"PAIL_TOKEN": "t"}
	for k, v := range s3 {
		vars[k] = v
	}
	c, err := Load(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if c.S3.Bucket != "pail" || c.S3.Region != "us-east-1" || c.S3.Addressing != "auto" {
		t.Errorf("storage's defaults: %+v", c.S3)
	}
	vars["PAIL_S3_ADDRESSING"] = "Virtual"
	if c, err := Load(env(vars)); err != nil || c.S3.Addressing != "virtual" {
		t.Errorf("PAIL_S3_ADDRESSING=Virtual: %+v, %v", c.S3, err)
	}
	vars["PAIL_S3_ADDRESSING"] = "sideways"
	if _, err := Load(env(vars)); err == nil || !strings.Contains(err.Error(), `"auto", "path" or "virtual"`) {
		t.Errorf("PAIL_S3_ADDRESSING=sideways: %v", err)
	}
	delete(vars, "PAIL_S3_ADDRESSING")
	if c.BaseDomain != "pail.lan" || c.MaxUploadSize != 100<<20 || c.MaxDeploys != 10 || c.MaxFunctionMemory != 1<<30 || c.MaxContainerMemory != 2<<30 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestSizes(t *testing.T) {
	for in, want := range map[string]int64{"100MB": 100 << 20, "1GB": 1 << 30, "1.5gb": 3 << 29, "512 KB": 512 << 10, "2048": 2048} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "lots", "-1MB", "0"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) should fail", in)
		}
	}
	if got := FormatSize(100 << 20); got != "100MB" {
		t.Errorf("FormatSize = %q", got)
	}
}

func TestACMEGoesTogether(t *testing.T) {
	vars := map[string]string{"PAIL_TOKEN": "t", "PAIL_ACME_DNS_PROVIDER": "cloudflare"}
	for k, v := range s3 {
		vars[k] = v
	}
	if _, err := Load(env(vars)); err == nil || !strings.Contains(err.Error(), "go together") {
		t.Fatalf("one ACME variable alone: got %v", err)
	}
	vars["PAIL_ACME_DNS_TOKEN"] = "secret"
	c, err := Load(env(vars))
	if err != nil || !c.ACME.Enabled() {
		t.Fatalf("both: %+v, %v", c.ACME, err)
	}
}

func TestOAuthApps(t *testing.T) {
	base := map[string]string{"PAIL_TOKEN": "t"}
	for k, v := range s3 {
		base[k] = v
	}
	with := func(extra map[string]string) (Config, error) {
		vars := map[string]string{}
		for k, v := range base {
			vars[k] = v
		}
		for k, v := range extra {
			vars[k] = v
		}
		return Load(env(vars))
	}

	c, err := with(map[string]string{
		"PAIL_OAUTH_GITHUB_CLIENT_ID": "gh-id", "PAIL_OAUTH_GITHUB_CLIENT_SECRET": "gh-secret",
		"PAIL_OAUTH_FORGEJO_CLIENT_ID": "fj-id", "PAIL_OAUTH_FORGEJO_CLIENT_SECRET": "fj-secret", "PAIL_OAUTH_FORGEJO_SERVER": "https://git.home.example/",
	})
	if err != nil || len(c.OAuth) != 2 || c.OAuth["github"].ClientID != "gh-id" || c.OAuth["forgejo"].Server != "https://git.home.example" {
		t.Fatalf("apps: %+v, %v", c.OAuth, err)
	}
	if _, err := with(map[string]string{"PAIL_OAUTH_GITHUB_CLIENT_ID": "gh-id"}); err == nil || !strings.Contains(err.Error(), "go together") {
		t.Errorf("an ID with no secret: %v", err)
	}
	if _, err := with(map[string]string{"PAIL_OAUTH_GITEA_CLIENT_ID": "x", "PAIL_OAUTH_GITEA_CLIENT_SECRET": "y"}); err == nil || !strings.Contains(err.Error(), "PAIL_OAUTH_GITEA_SERVER") {
		t.Errorf("Gitea with no server: %v", err)
	}
}

func TestAllowLANListsPails(t *testing.T) {
	load := func(list string) (Config, error) {
		vars := map[string]string{"PAIL_TOKEN": "t", "PAIL_ALLOW_LAN": list}
		for k, v := range s3 {
			vars[k] = v
		}
		return Load(env(vars))
	}
	if c, err := load(""); err != nil || len(c.AllowLAN) != 0 {
		t.Errorf("unset: %v, %v", c.AllowLAN, err)
	}
	if c, err := load(" Media, backups ,,"); err != nil || strings.Join(c.AllowLAN, "|") != "media|backups" {
		t.Errorf("a list: %v, %v", c.AllowLAN, err)
	}
	if _, err := load("media=10.0.0.5"); err == nil || !strings.Contains(err.Error(), "PAIL_ALLOW_LAN") {
		t.Errorf("something that isn't a pail's name: got %v", err)
	}
}

func TestRuntimeIsMicroVMsContainersOrWhicheverRuns(t *testing.T) {
	load := func(runtime string) (Config, error) {
		vars := map[string]string{"PAIL_TOKEN": "t", "PAIL_RUNTIME": runtime}
		for k, v := range s3 {
			vars[k] = v
		}
		return Load(env(vars))
	}
	for in, want := range map[string]string{"": "auto", "auto": "auto", "firecracker": "firecracker", "container": "container", "Docker": "container", "podman": "container"} {
		c, err := load(in)
		if err != nil || c.Runtime != want {
			t.Errorf("PAIL_RUNTIME=%q: runtime %q, %v; want %q", in, c.Runtime, err, want)
		}
		if c.ContainerSocket != "/var/run/docker.sock" || c.ContainerNetwork != "pail" {
			t.Errorf("PAIL_RUNTIME=%q: engine defaults: %+v", in, c)
		}
	}
	if _, err := load("kubernetes"); err == nil || !strings.Contains(err.Error(), "PAIL_RUNTIME") {
		t.Errorf("an unknown runtime: got %v", err)
	}
}

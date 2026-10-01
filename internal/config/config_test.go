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
	if c.BaseDomain != "pail.lan" || c.MaxUploadSize != 100<<20 || c.MaxDeploys != 10 || c.MaxFunctionMemory != 1<<30 {
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

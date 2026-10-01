package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// configFile is ~/.pail/config: one profile per Pail installation.
type configFile struct {
	Default  string             `toml:"default,omitempty"`
	Profiles map[string]profile `toml:"profiles"`
}

type profile struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
	// CA is a root certificate to trust for this installation.
	CA string `toml:"ca,omitempty"`
}

// configPath is PAIL_CONFIG, else ~/.pail/config.
func (a *app) configPath() string {
	if p := a.env.Getenv("PAIL_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(a.env.Home, ".pail", "config")
}

// shortPath prints a path the way people write it.
func (a *app) shortPath(p string) string {
	if rel, err := filepath.Rel(a.env.Home, p); err == nil && a.env.Home != "" && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

// loadConfig reads the config file. A missing file is an empty config.
func (a *app) loadConfig() (configFile, error) {
	cfg := configFile{Profiles: map[string]profile{}}
	path := a.configPath()
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, usagef("Can't read %s: %v.", a.shortPath(path), err)
	}
	// The file holds tokens, so it must be the user's alone.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return cfg, usagef("%s can be read by other users, and it holds your tokens. Run chmod 600 %s.", a.shortPath(path), a.shortPath(path))
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, usagef("%s isn't valid TOML: %v.", a.shortPath(path), err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]profile{}
	}
	return cfg, nil
}

// saveConfig writes the config file as 0600, in a folder that is 0700.
func (a *app) saveConfig(cfg configFile) error {
	path := a.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return usagef("Can't create %s: %v.", a.shortPath(filepath.Dir(path)), err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return usagef("Can't write %s: %v.", a.shortPath(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return usagef("Can't write %s: %v.", a.shortPath(path), err)
	}
	return nil
}

func (c configFile) names() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// target is the installation a command talks to.
type target struct {
	// name is the profile's name, or "PAIL_URL" for the one built from the
	// environment.
	name string
	profile
}

// resolve picks the installation, stopping at the first match:
// --profile, PAIL_PROFILE, PAIL_URL, the default, the only profile.
func (a *app) resolve() (target, error) {
	named := a.flags.profile
	if named == "" {
		named = a.env.Getenv("PAIL_PROFILE")
	}
	if named == "" {
		if url := a.env.Getenv("PAIL_URL"); url != "" {
			token := strings.TrimSpace(a.env.Getenv("PAIL_TOKEN"))
			if token == "" {
				return target{}, usagef("PAIL_URL is set but PAIL_TOKEN isn't. Set both, or neither.")
			}
			return target{name: "PAIL_URL", profile: profile{URL: normalizeURL(url), Token: token, CA: a.env.Getenv("PAIL_CA")}}, nil
		}
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return target{}, err
	}
	path := a.shortPath(a.configPath())
	names := cfg.names()
	if named == "" {
		named = cfg.Default
	}
	if named == "" {
		switch len(names) {
		case 0:
			return target{}, usagef("No Pail installation yet. Run pail login <url>.")
		case 1:
			named = names[0]
		default:
			return target{}, usagef("More than one Pail installation in %s. Pick one with --profile %s.", path, strings.Join(names, " or --profile "))
		}
	}
	p, ok := cfg.Profiles[named]
	if !ok {
		if len(names) == 0 {
			return target{}, usagef("No profile called %s. Run pail login <url> --profile %s.", named, named)
		}
		return target{}, usagef("No profile called %s in %s. You have: %s.", named, path, strings.Join(names, ", "))
	}
	if p.URL == "" || p.Token == "" {
		return target{}, usagef("Profile %s in %s needs a url and a token. Run pail login <url> --profile %s.", named, path, named)
	}
	p.URL = normalizeURL(p.URL)
	return target{name: named, profile: p}, nil
}

// normalizeURL gives a bare host a scheme and drops a trailing slash.
func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return strings.TrimRight(u, "/")
}

// expandHome turns ~/x into a path under the home folder.
func (a *app) expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(a.env.Home, rest)
	}
	return p
}

func usagef(format string, args ...any) error {
	return &exitError{code: ExitUsage, msg: fmt.Sprintf(format, args...)}
}

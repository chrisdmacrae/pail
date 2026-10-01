package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chrisdmacrae/pail/internal/config"
)

// up packs a folder, deploys it as a pail, follows the log and prints the
// pail's URL.
func (a *app) up(args []string) error {
	if len(args) > 1 {
		return usagef("pail up takes one folder: pail up ./dist.")
	}
	shown := "."
	if len(args) == 1 {
		shown = args[0]
	}
	dir := shown
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.env.Cwd, dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return usagef("No folder at %s. Point pail up at the folder you build into.", shown)
	}
	_, noIndex := os.Stat(filepath.Join(dir, "index.html"))
	manifest, noManifest := os.ReadFile(filepath.Join(dir, "pail.json"))
	// A project with a package.json may be one Pail builds; the server says.
	_, noPackage := os.Stat(filepath.Join(dir, "package.json"))
	if noIndex != nil && noManifest != nil && noPackage != nil {
		return usagef("No index.html in %s. Point Pail at the folder that has it.", shown)
	}

	name, err := a.pailName(dir, manifest)
	if err != nil {
		return err
	}
	c, err := a.connect()
	if err != nil {
		return err
	}
	var info apiInfo
	if err := c.get("/api/v1/info", &info); err != nil {
		return err
	}

	// A project that builds from its workspace's lockfile is no use to the
	// server alone: the workspace goes too, and the server is told which
	// folder of it to deploy.
	packed, folder := dir, ""
	if root, lockfile := workspace(dir, manifest); root != "" && info.Builds.Available && info.Builds.Workspaces {
		if rel, err := filepath.Rel(root, dir); err == nil {
			packed, folder = root, filepath.ToSlash(rel)
			if !a.flags.quiet {
				fmt.Fprintf(a.env.Stderr, "%s builds from the %s in %s, so that folder is sent with it.\n", shown, lockfile, root)
			}
		}
	}

	archive, size, err := pack(packed)
	if err != nil {
		return usagef("Couldn't pack %s: %v.", shown, err)
	}
	defer os.Remove(archive)
	if limit := info.Limits.MaxUploadSize; limit > 0 && size > limit {
		return usagef("%s packs to %s, over the %s this Pail takes. Raise PAIL_MAX_UPLOAD_SIZE on the server to send more.", shown, config.FormatSize(size), config.FormatSize(limit))
	}

	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	target := "/api/v1/pails/" + name + "/deploys?source=cli"
	if folder != "" {
		target += "&dir=" + url.QueryEscape(folder)
	}
	resp, err := c.do("POST", target, f, size, "application/gzip")
	if err != nil {
		return err
	}
	var started apiDeploy
	err = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if err != nil {
		started = apiDeploy{}
	}
	return a.follow(c, name, started)
}

// follow reads a deploy's log to the end, then prints the pail's URL, or
// exits 1 if the deploy failed.
func (a *app) follow(c *client, name string, started apiDeploy) error {
	if started.ID == "" {
		return &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s started a deploy but didn't say which. Run pail logs %s.", c.target.URL, name)}
	}

	// Progress goes to stderr, so stdout carries only the result.
	d, err := c.streamLog(name, started.ID, true, func(l apiLine) {
		if !a.flags.quiet {
			a.printLine(a.env.Stderr, l)
		}
	})
	if err != nil {
		return err
	}
	if d.State != "ok" {
		if a.flags.json {
			a.printJSON(d)
		}
		msg := ""
		if a.flags.quiet {
			msg = d.Error // the log, which says why, wasn't shown
		}
		return &exitError{code: ExitDeployFailed, msg: msg}
	}
	if a.flags.json {
		return a.printJSON(d)
	}
	fmt.Fprintln(a.env.Stdout, d.URL)
	return nil
}

var (
	// A pail name is one DNS label, as the server checks it.
	validName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	notName   = regexp.MustCompile(`[^a-z0-9]+`)
)

// outputFolders are names a build writes into; never a pail's name.
var outputFolders = map[string]bool{
	"dist": true, "build": true, "out": true, "output": true, "public": true,
	"_site": true, "www": true,
}

// pailName picks the pail's name: --name, else name in pail.json, else the
// git repo's name, with the folder's after it for a folder that isn't the
// repo's top, else the current folder's name.
func (a *app) pailName(dir string, manifest []byte) (string, error) {
	const rule = "Pail names are lowercase letters, numbers and dashes, like my-site."
	if a.flags.name != "" {
		if !validName.MatchString(a.flags.name) {
			return "", usagef("%s isn't a name Pail can use. %s", a.flags.name, rule)
		}
		return a.flags.name, nil
	}
	var pj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(manifest, &pj) == nil && pj.Name != "" {
		if !validName.MatchString(pj.Name) {
			return "", usagef("pail.json names this %s, which Pail can't use. %s", pj.Name, rule)
		}
		return pj.Name, nil
	}

	if root := gitRoot(dir); root != "" {
		name := filepath.Base(root)
		// A repo may hold more than one pail. Each is named for its folder
		// too, so one doesn't deploy over another.
		if folder := pailFolder(root, dir); folder != "" {
			name += "-" + folder
		}
		if name = slug(name); name != "" {
			return name, nil
		}
	}
	// The current folder, stepping out of anything named like build output.
	for at := a.env.Cwd; ; at = filepath.Dir(at) {
		base := filepath.Base(at)
		if !outputFolders[strings.ToLower(base)] {
			if name := slug(base); name != "" {
				return name, nil
			}
		}
		if filepath.Dir(at) == at {
			return "", usagef("Can't guess a name for this pail. Give it one: pail up --name my-site.")
		}
	}
}

// lockfiles are what the package managers Pail builds with leave at the top
// of a project, or of a workspace of several.
var lockfiles = []string{"pnpm-lock.yaml", "package-lock.json", "yarn.lock"}

func lockfileIn(dir string) string {
	for _, name := range lockfiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

// workspace finds the workspace a project is part of: the nearest folder
// above dir, no further up than its git repo's top, that holds a lockfile.
// It returns "" for a project with a lockfile of its own, one outside a git
// repo, and one Pail wouldn't build: no build script, or server code, which
// is built from the folder alone.
func workspace(dir string, manifest []byte) (root, lockfile string) {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil || json.Unmarshal(b, &pkg) != nil || pkg.Scripts["build"] == "" {
		return "", ""
	}
	var pj struct {
		Functions  map[string]json.RawMessage `json:"functions"`
		Containers map[string]json.RawMessage `json:"containers"`
	}
	if json.Unmarshal(manifest, &pj) == nil && len(pj.Functions)+len(pj.Containers) > 0 {
		return "", ""
	}
	top := gitRoot(dir)
	if top == "" || top == dir || lockfileIn(dir) != "" {
		return "", ""
	}
	for at := filepath.Dir(dir); ; at = filepath.Dir(at) {
		if name := lockfileIn(at); name != "" {
			return at, name
		}
		if at == top || filepath.Dir(at) == at {
			return "", ""
		}
	}
}

// gitRoot is the folder holding the .git that dir sits under, or "".
func gitRoot(dir string) string {
	for at := dir; ; at = filepath.Dir(at) {
		if _, err := os.Stat(filepath.Join(at, ".git")); err == nil {
			return at
		}
		if filepath.Dir(at) == at {
			return ""
		}
	}
}

// pailFolder is the name of the folder under a repo's root that dir deploys,
// stepping out of anything named like build output, or "" when that is the
// root itself.
func pailFolder(root, dir string) string {
	at := dir
	for at != root && outputFolders[strings.ToLower(filepath.Base(at))] {
		at = filepath.Dir(at)
	}
	if at == root {
		return ""
	}
	return filepath.Base(at)
}

// slug makes a folder name into a pail name, or "" if nothing is left.
func slug(s string) string {
	s = strings.Trim(notName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// packSkip are folders that are never part of a site.
var packSkip = map[string]bool{".git": true, "node_modules": true}

// pack writes dir's files to a temporary .tar.gz and returns its path and
// size. Paths inside are relative to dir.
func pack(dir string) (path string, size int64, err error) {
	tmp, err := os.CreateTemp("", "pail-up-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)

	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if packSkip[d.Name()] && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		// Stat follows a symlink to the file it points at.
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.CopyN(tw, f, info.Size())
		return err
	})
	if err != nil {
		return "", 0, err
	}
	if err = tw.Close(); err != nil {
		return "", 0, err
	}
	if err = gz.Close(); err != nil {
		return "", 0, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return "", 0, err
	}
	return tmp.Name(), info.Size(), nil
}

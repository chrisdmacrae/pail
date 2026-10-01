package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	if noIndex != nil && noManifest != nil {
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

	archive, size, err := pack(dir)
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
	resp, err := c.do("POST", "/api/v1/pails/"+name+"/deploys?source=cli", f, size)
	if err != nil {
		return err
	}
	var started apiDeploy
	err = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if err != nil || started.ID == "" {
		return &exitError{code: ExitUnreachable, msg: fmt.Sprintf("%s took the upload but didn't say which deploy it became. Run pail logs %s.", c.target.URL, name)}
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
// git repo's name, else the current folder's name.
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
		if name := slug(filepath.Base(root)); name != "" {
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

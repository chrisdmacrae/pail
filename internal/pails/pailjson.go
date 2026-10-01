package pails

import (
	"encoding/json"
	"path"
	"strings"
)

// pailJSON is the part of pail.json Pail reads so far: what to serve as
// files. Functions and containers are recognised so a deploy that needs them
// is refused rather than half-served.
type pailJSON struct {
	Name       string                     `json:"name"`
	Static     string                     `json:"static"`
	Functions  map[string]json.RawMessage `json:"functions"`
	Containers map[string]json.RawMessage `json:"containers"`
	Routes     []struct {
		Path     string `json:"path"`
		To       string `json:"to"`
		Fallback string `json:"fallback"`
	} `json:"routes"`
}

// staticConfig is what a static deploy takes from pail.json.
type staticConfig struct {
	root     string // "" or "build/"
	fallback string // relative to root
}

func parsePailJSON(b []byte) (staticConfig, error) {
	var c staticConfig
	var pj pailJSON
	if err := json.Unmarshal(b, &pj); err != nil {
		return c, userErrorf("pail.json isn't valid JSON: %v.", err)
	}
	if len(pj.Functions) > 0 || len(pj.Containers) > 0 {
		return c, userErrorf("pail.json declares functions or containers, and this Pail only serves static files so far. Take them out to deploy the files.")
	}

	root, err := relPath(pj.Static)
	if err != nil {
		return c, userErrorf("pail.json: static must be a folder inside the upload, like ./dist.")
	}
	if root != "" {
		c.root = root + "/"
	}

	for _, r := range pj.Routes {
		if r.To != "static" {
			return c, userErrorf("pail.json routes %s to %q, and this Pail only serves static files so far. Use \"static\".", r.Path, r.To)
		}
		if r.Fallback != "" && c.fallback == "" {
			if c.fallback, err = relPath(r.Fallback); err != nil || c.fallback == "" {
				return c, userErrorf("pail.json: fallback must be a file inside the static folder, like index.html.")
			}
		}
	}
	return c, nil
}

// relPath cleans a path from pail.json. "" and "." mean the top.
func relPath(p string) (string, error) {
	for _, seg := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if seg == ".." {
			return "", userErrorf("path leaves the upload")
		}
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/"), nil
}

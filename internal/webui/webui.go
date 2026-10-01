// Package webui holds the built web UI, so pail-server is one binary.
//
// The UI is built from web/ui into dist/ (make ui). A checkout that hasn't
// built it still compiles: dist/ then holds only a placeholder, and FS
// reports that there is no UI to serve.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI's files, or nil if the UI hasn't been built.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}

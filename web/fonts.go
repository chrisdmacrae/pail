// Package web holds what pail-server takes from the design system as it is,
// without the web UI's build: the fonts for the pages it writes itself.
package web

import "embed"

// Fonts are the design system's typefaces, under design-system/fonts.
//
//go:embed design-system/fonts/*.woff2
var Fonts embed.FS

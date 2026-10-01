package githost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Detection is what Pail makes of a repo before deploying it.
type Detection struct {
	// Deployable says Pail can put this repo live as it stands.
	Deployable bool `json:"deployable"`
	// Summary says what Pail found, in a few words.
	Summary string `json:"summary"`
}

// frameworks names a project by a package it depends on, most telling first.
var frameworks = []struct{ pkg, name string }{
	{"astro", "Astro site"}, {"next", "Next.js app"}, {"nuxt", "Nuxt app"},
	{"@sveltejs/kit", "SvelteKit app"}, {"vite", "Vite app"}, {"react-scripts", "React app"},
	{"@11ty/eleventy", "Eleventy site"}, {"gatsby", "Gatsby site"},
}

// Detect looks at the top of a repo and says whether Pail can serve it. Pail
// serves files as they are in the repo; a project that has to be built
// first is recognised and declined.
func Detect(ctx context.Context, c Client, repo, branch string) (Detection, error) {
	read := func(path string) ([]byte, bool, error) {
		b, err := c.ReadFile(ctx, repo, branch, path)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return b, err == nil, err
	}

	pkg, hasPkg, err := read("package.json")
	if err != nil {
		return Detection{}, err
	}
	if hasPkg {
		var p struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(pkg, &p) == nil && p.Scripts["build"] != "" {
			what := "Node project"
			for _, f := range frameworks {
				if p.Dependencies[f.pkg] != "" || p.DevDependencies[f.pkg] != "" {
					what = f.name
					break
				}
			}
			return Detection{Summary: what + " · needs a build, which Pail can’t run yet"}, nil
		}
	}

	manifest, hasManifest, err := read("pail.json")
	if err != nil {
		return Detection{}, err
	}
	if hasManifest {
		var m struct {
			Static     string                     `json:"static"`
			Functions  map[string]json.RawMessage `json:"functions"`
			Containers map[string]json.RawMessage `json:"containers"`
		}
		switch {
		case json.Unmarshal(manifest, &m) != nil:
			return Detection{Summary: "pail.json isn’t valid JSON"}, nil
		case len(m.Functions) > 0 || len(m.Containers) > 0:
			return Detection{Summary: "pail.json has functions or containers, which Pail can’t run yet"}, nil
		}
		static := strings.TrimSpace(m.Static)
		if static == "" || static == "." || static == "./" {
			static = "the top folder"
		}
		return Detection{Deployable: true, Summary: "Static files · pail.json serves " + static}, nil
	}

	if _, hasIndex, err := read("index.html"); err != nil {
		return Detection{}, err
	} else if hasIndex {
		return Detection{Deployable: true, Summary: "Static files · index.html at the top"}, nil
	}
	return Detection{Summary: "No index.html at the top, and no pail.json"}, nil
}

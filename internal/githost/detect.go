package githost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Detect looks at the top of a repo and says whether Pail can serve it.
// canBuild says this Pail can build a project first; where it can't, a
// project that needs building is recognised and declined.
func Detect(ctx context.Context, c Client, repo, branch string, canBuild bool) (Detection, error) {
	read := func(path string) ([]byte, bool, error) {
		b, err := c.ReadFile(ctx, repo, branch, path)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return b, err == nil, err
	}

	// pail.json's server code decides first: a project with containers is
	// built by its Dockerfiles, whatever its package.json says.
	manifest, hasManifest, err := read("pail.json")
	if err != nil {
		return Detection{}, err
	}
	var m struct {
		Static     string                     `json:"static"`
		Functions  map[string]json.RawMessage `json:"functions"`
		Containers map[string]struct {
			Image string `json:"image"`
		} `json:"containers"`
	}
	if hasManifest {
		switch {
		case json.Unmarshal(manifest, &m) != nil:
			return Detection{Summary: "pail.json isn’t valid JSON"}, nil
		case len(m.Functions) > 0:
			return Detection{Summary: "pail.json has functions, which Pail can’t run yet"}, nil
		case len(m.Containers) > 0 && !canBuild:
			return Detection{Summary: "pail.json has containers, which this Pail can’t run"}, nil
		case len(m.Containers) == 1:
			for _, c := range m.Containers {
				if c.Image != "" {
					return Detection{Deployable: true, Summary: "A container · Pail pulls " + c.Image + " and runs it"}, nil
				}
			}
			return Detection{Deployable: true, Summary: "A container · Pail builds its Dockerfile and runs it"}, nil
		case len(m.Containers) > 1:
			return Detection{Deployable: true, Summary: fmt.Sprintf("%d containers · Pail builds or pulls each one and runs them", len(m.Containers))}, nil
		}
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
			if canBuild {
				return Detection{Deployable: true, Summary: what + " · Pail builds it on every deploy"}, nil
			}
			return Detection{Summary: what + " · needs a build, which this Pail can’t run"}, nil
		}
	}

	if hasManifest {
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

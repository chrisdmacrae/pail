package githost

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
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

// Detect looks at the top of a repo, or at the folder dir of one that holds
// several pails, and says whether Pail can serve it. canBuild says this Pail
// can build a project first; where it can't, a project that needs building is
// recognised and declined.
func Detect(ctx context.Context, c Client, repo, branch, dir string, canBuild bool) (Detection, error) {
	where := "at the top"
	if dir != "" {
		where = "in " + dir
	}
	read := func(file string) ([]byte, bool, error) {
		b, err := c.ReadFile(ctx, repo, branch, path.Join(dir, file))
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
		case len(m.Functions)+len(m.Containers) > 0 && !canBuild:
			return Detection{Summary: "pail.json has server code, which this Pail can’t run"}, nil
		case len(m.Functions) > 0 && len(m.Containers) > 0:
			return Detection{Deployable: true, Summary: "Functions and containers · Pail builds and runs them"}, nil
		case len(m.Functions) == 1:
			return Detection{Deployable: true, Summary: "A function · Pail builds it and runs it on request"}, nil
		case len(m.Functions) > 1:
			return Detection{Deployable: true, Summary: fmt.Sprintf("%d functions · Pail builds them and runs them on request", len(m.Functions))}, nil
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
			static = cmp.Or(dir, "the top folder")
		}
		return Detection{Deployable: true, Summary: "Static files · pail.json serves " + static}, nil
	}

	if _, hasIndex, err := read("index.html"); err != nil {
		return Detection{}, err
	} else if hasIndex {
		return Detection{Deployable: true, Summary: "Static files · index.html " + where}, nil
	}
	return Detection{Summary: "No index.html " + where + ", and no pail.json"}, nil
}

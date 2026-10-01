package pails

import (
	"path"
	"sort"
	"strings"
)

// language is one a function can be written in: how Pail recognises it,
// builds it, and runs it. A function's source is at /work/src, both when it
// is built and when it runs.
type language struct {
	name string
	// buildImage is the image the build runs in, and runImage the one the
	// function runs in. They differ where running needs less than building.
	buildImage, runImage string
	// detect are the files that mark a folder as this language, and ext the
	// extension that marks a single file.
	detect []string
	ext    []string
	// main is the file run when the source is a folder.
	main string
	// script builds the source, or is "" when there is nothing to build.
	// single is the file's name when the source is one file, else "".
	script func(files map[string]bool, single string) string
	// run is the command that answers a request.
	run func(files map[string]bool, main string) []string
	env []string
	// warm is run once before the function is snapshotted, to have the
	// language's own files read from disk already when a copy wakes.
	warm []string
}

const functionDir = "/work/src"

// languages are tried in this order, so a folder with a requirements.txt
// and a package.json is Python's.
var languages = []language{
	{
		name: "python", buildImage: "python:3.13-slim", runImage: "python:3.13-slim",
		detect: []string{"requirements.txt", "main.py"}, ext: []string{".py"}, main: "main.py",
		script: func(files map[string]bool, _ string) string {
			if !files["requirements.txt"] {
				return ""
			}
			return `echo "→ pip install -r requirements.txt"
pip install --no-cache-dir --disable-pip-version-check --root-user-action=ignore --target .deps -r requirements.txt
`
		},
		run:  func(_ map[string]bool, main string) []string { return []string{"python3", main} },
		env:  []string{"PYTHONPATH=" + functionDir + "/.deps", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"},
		warm: []string{"python3", "-c", "import json, os, sys"},
	},
	{
		name: "node", buildImage: "node:22-slim", runImage: "node:22-slim",
		detect: []string{"package.json", "index.js"}, ext: []string{".js", ".mjs", ".cjs"}, main: "index.js",
		script: func(files map[string]bool, _ string) string {
			if !files["package.json"] {
				return ""
			}
			install := `echo "→ npm install"; npm install --omit=dev --no-audit --no-fund`
			if files["package-lock.json"] {
				install = `echo "→ npm ci"; npm ci --omit=dev --no-audit --no-fund`
			}
			// What to run is worked out once, here, not on every request.
			return `export npm_config_cache=/work/home/.npm npm_config_update_notifier=false
` + install + `
node -p 'require("./package.json").main || "index.js"' > .pail-main
`
		},
		run: func(files map[string]bool, main string) []string {
			if files["package.json"] {
				return []string{"sh", "-c", `exec node "$(cat .pail-main)"`}
			}
			return []string{"node", main}
		},
		env:  []string{"NODE_ENV=production"},
		warm: []string{"node", "-e", "require('http')"},
	},
	{
		name: "ruby", buildImage: "ruby:3.4-slim", runImage: "ruby:3.4-slim",
		detect: []string{"Gemfile", "main.rb"}, ext: []string{".rb"}, main: "main.rb",
		script: func(files map[string]bool, _ string) string {
			if !files["Gemfile"] {
				return ""
			}
			return `echo "→ bundle install"
bundle config set --local path vendor/bundle
bundle install
`
		},
		run: func(files map[string]bool, main string) []string {
			if files["Gemfile"] {
				return []string{"bundle", "exec", "ruby", main}
			}
			return []string{"ruby", main}
		},
		warm: []string{"ruby", "-e", "require 'json'"},
	},
	{
		// A Go program is one file with nothing to link against, so it runs
		// in a far smaller image than it is built in.
		name: "go", buildImage: "golang:1.25-alpine", runImage: "alpine:3",
		detect: []string{"go.mod"}, ext: []string{".go"},
		script: func(_ map[string]bool, single string) string {
			what := "."
			if single != "" {
				what = single
			}
			return `export GOCACHE=/work/home/gocache GOPATH=/work/home/go CGO_ENABLED=0 GOFLAGS=-buildvcs=false
echo "→ go build -o fn"
go build -o fn ` + what + `
`
		},
		run:  func(map[string]bool, string) []string { return []string{"./fn"} },
		warm: []string{"sh", "-c", "cat fn > /dev/null"},
	},
	{
		name: "rust", buildImage: "rust:1-slim-bookworm", runImage: "debian:bookworm-slim",
		detect: []string{"Cargo.toml"}, ext: []string{".rs"},
		script: func(_ map[string]bool, single string) string {
			if single != "" {
				return `echo "→ rustc -O"
rustc -O -o fn ` + single + `
`
			}
			return `export CARGO_HOME=/work/home/cargo
echo "→ cargo build --release"
cargo build --release
bin=$(find target/release -maxdepth 1 -type f -perm -u+x | head -1)
[ -n "$bin" ] || { echo "the build left no program in target/release"; exit 1; }
cp "$bin" fn
rm -rf target
`
		},
		run:  func(map[string]bool, string) []string { return []string{"./fn"} },
		warm: []string{"sh", "-c", "cat fn > /dev/null"},
	},
	{
		name: "shell", runImage: "alpine:3",
		detect: []string{"main.sh"}, ext: []string{".sh"}, main: "main.sh",
		script: func(map[string]bool, string) string { return "" },
		run:    func(_ map[string]bool, main string) []string { return []string{"sh", main} },
		warm:   []string{"sh", "-c", "true"},
	},
}

func languageNames() string {
	names := make([]string, len(languages))
	for i, l := range languages {
		names[i] = l.name
	}
	return strings.Join(names, ", ")
}

func languageNamed(name string) *language {
	for i := range languages {
		if languages[i].name == name {
			return &languages[i]
		}
	}
	return nil
}

// detectLanguage picks the language of a function's source: files are its
// files, relative to its folder, and single is its name when it is one file.
func detectLanguage(files map[string]bool, single string) *language {
	for i := range languages {
		l := &languages[i]
		if single != "" {
			for _, ext := range l.ext {
				if strings.EqualFold(path.Ext(single), ext) {
					return l
				}
			}
			continue
		}
		for _, file := range l.detect {
			if files[file] {
				return l
			}
		}
	}
	return nil
}

// plan works out how a function is built and run from its source.
type functionPlan struct {
	lang   *language
	script string
	argv   []string
	env    []string
}

// planFunction decides how to build and run a function. names are every
// file in the upload.
func planFunction(name string, fc functionConfig, names map[string]bool) (functionPlan, error) {
	var plan functionPlan
	files := map[string]bool{}
	single := ""
	if names[fc.src] {
		single = path.Base(fc.src)
		files[single] = true
	} else {
		prefix := fc.src + "/"
		if fc.src == "" {
			prefix = ""
		}
		for n := range names {
			if rel, ok := strings.CutPrefix(n, prefix); ok && rel != "" {
				files[rel] = true
			}
		}
	}
	if len(files) == 0 {
		return plan, userErrorf("pail.json: %s's src is ./%s, and the upload has nothing there.", name, fc.src)
	}

	if fc.lang != "" {
		plan.lang = languageNamed(fc.lang)
	} else if plan.lang = detectLanguage(files, single); plan.lang == nil {
		var found []string
		for f := range files {
			if !strings.Contains(f, "/") {
				found = append(found, f)
			}
		}
		sort.Strings(found)
		if len(found) > 6 {
			found = append(found[:6], "…")
		}
		return plan, userErrorf("Pail can't tell what language %s is in: ./%s has %s. Say with \"lang\" in pail.json: %s.", name, fc.src, strings.Join(found, ", "), languageNames())
	}
	l := plan.lang
	main := l.main
	if single != "" {
		main = single
	}
	plan.script = l.script(files, single)
	plan.argv = l.run(files, main)
	plan.env = l.env
	if len(fc.cmd) > 0 {
		plan.argv = fc.cmd
	} else if main != "" && !files[main] && single == "" && l.main != "" && !(l.name == "node" && files["package.json"]) {
		return plan, userErrorf("%s is %s, which runs %s, and ./%s has no such file. Add it, or say what to run with \"cmd\" in pail.json.", name, l.name, l.main, fc.src)
	}
	return plan, nil
}

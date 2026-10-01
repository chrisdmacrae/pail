//go:build linux

package microvm

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// What a site build gets.
const (
	nodeImage    = "docker.io/library/node:22-slim"
	buildVCPUs   = 2
	buildMemMB   = 2048
	buildWorkMB  = 8192
	buildTimeout = 15 * time.Minute
)

// buildScript installs a Node project's dependencies and builds it. The root
// filesystem is read-only, so everything a tool caches goes under /work.
// PAIL_DIR, when set, is the folder of the source the project is in.
const buildScript = `set -e
export HOME=/work/home CI=true NO_COLOR=1
export npm_config_cache=/work/home/.npm npm_config_update_notifier=false npm_config_fund=false
export YARN_CACHE_FOLDER=/work/home/.yarn PNPM_HOME=/work/home/.pnpm
mkdir -p "$HOME"

# The project: the top of the source, or one folder of a repo with several.
proj=/work/src${PAIL_DIR:+/$PAIL_DIR}
cd "$proj"

# Dependencies install where the lockfile is: here, or in a folder above for
# a project that is part of a workspace.
has_lock() { [ -f "$1/pnpm-lock.yaml" ] || [ -f "$1/package-lock.json" ] || [ -f "$1/yarn.lock" ]; }
root=$proj
if ! has_lock "$proj"; then
  at=$proj
  while [ "$at" != /work/src ]; do
    at=$(dirname "$at")
    if has_lock "$at"; then root=$at; break; fi
  done
fi
if [ "$root" != "$proj" ]; then
  above=${root#/work/src}
  echo "→ part of a workspace · installing from ${above:+.}${above:-the top of the repo}"
fi

# Which package manager: the one package.json names, here or at the
# workspace's top, else the one whose lockfile is there. With two lockfiles
# and nothing said, npm's wins.
named() { node -p 'try { (require(process.argv[1] + "/package.json").packageManager || "").split("@")[0] } catch (e) { "" }' "$1"; }
pm=$(named "$proj")
[ -n "$pm" ] || pm=$(named "$root")
if [ -z "$pm" ]; then
  if [ -f "$root/pnpm-lock.yaml" ]; then pm=pnpm
  elif [ -f "$root/package-lock.json" ]; then pm=npm
  elif [ -f "$root/yarn.lock" ]; then pm=yarn
  else pm=npm
  fi
fi

case "$pm" in
  pnpm)
    echo "→ pnpm install"; (cd "$root" && npx --yes pnpm@latest install --frozen-lockfile)
    echo "→ pnpm run build"; npx --yes pnpm@latest run build ;;
  yarn)
    echo "→ yarn install"; (cd "$root" && npx --yes yarn@1 install --frozen-lockfile)
    echo "→ yarn build"; npx --yes yarn@1 run build ;;
  *)
    if [ -f "$root/package-lock.json" ]; then echo "→ npm ci"; (cd "$root" && npm ci); else echo "→ npm install"; (cd "$root" && npm install); fi
    echo "→ npm run build"; npm run build ;;
esac
`

// outputs are where builds leave a site, most telling first. public comes
// last: in many projects it holds source files, not the result.
var outputs = []string{"dist", "build", "out", "_site", ".output/public", "public"}

// BuildSite builds a Node project in a throwaway microVM that can reach the
// internet for dependencies and nothing else, and returns the folder the
// built site is in.
func (r *Runner) BuildSite(ctx context.Context, req BuildRequest) (BuildResult, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	if err := os.MkdirAll(filepath.Join(r.cfg.Dir, "builds"), 0o755); err != nil {
		return BuildResult{}, err
	}
	dir, err := os.MkdirTemp(filepath.Join(r.cfg.Dir, "builds"), "b-")
	if err != nil {
		return BuildResult{}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(err error) (BuildResult, error) {
		cleanup()
		return BuildResult{}, err
	}

	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "src"), 0o755); err != nil {
		return fail(err)
	}
	if err := req.Fill(filepath.Join(stage, "src")); err != nil {
		return fail(err)
	}
	image, err := r.Image(ctx, nodeImage, log)
	if err != nil {
		return fail(err)
	}

	// Where the project is inside the source, for the script and for
	// finding what it built.
	project := strings.Trim(path.Clean("/"+req.Dir), "/")
	var env []string
	if project != "" {
		env = []string{"PAIL_DIR=" + project}
	}
	done, err := r.Run(ctx, Job{
		Image: image, Stage: stage, WorkMB: buildWorkMB,
		Argv: []string{"/bin/sh", "-c", buildScript}, Env: env, Dir: "/",
		VCPUs: buildVCPUs, MemMB: buildMemMB, Network: true, Log: log,
	})
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fail(fmt.Errorf("the build ran for %s and was stopped", buildTimeout))
	case err != nil:
		return fail(err)
	case done.ExitCode != 0:
		return fail(fmt.Errorf("the build exited with status %d", done.ExitCode))
	}

	// Find where the site landed: where pail.json says, or the first of the
	// usual places that has an index.html. A build that makes functions too
	// leaves a pail.json of its own there instead, to say what is what.
	candidates := outputs
	if req.Static != "" {
		candidates = []string{strings.Trim(path.Clean("/"+req.Static), "/")}
	}
	for _, out := range candidates {
		built := "/src/" + path.Join(project, out)
		page, _ := r.read(done.Work, built+"/index.html")
		if page == "" {
			page, _ = r.read(done.Work, built+"/pail.json")
		}
		if page == "" {
			continue
		}
		if err := r.Extract(done.Work, built, filepath.Join(dir, "out")); err != nil {
			return fail(err)
		}
		os.Remove(done.Work) // the site is out; the rest was scaffolding
		return BuildResult{Dir: filepath.Join(dir, "out", path.Base(out)), Output: out, Cleanup: cleanup}, nil
	}
	if req.Static != "" {
		return fail(fmt.Errorf("the build finished, but left no index.html in ./%s, where pail.json says the site is", candidates[0]))
	}
	return fail(fmt.Errorf("the build finished, but Pail found no index.html in any of: %s. Say where the site lands with \"static\" in pail.json", strings.Join(outputs, ", ")))
}

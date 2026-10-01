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
const buildScript = `set -e
cd /work/src
export HOME=/work/home CI=true NO_COLOR=1
export npm_config_cache=/work/home/.npm npm_config_update_notifier=false npm_config_fund=false
export YARN_CACHE_FOLDER=/work/home/.yarn PNPM_HOME=/work/home/.pnpm
mkdir -p "$HOME"

# Which package manager: the one package.json names, else the one whose
# lockfile is here. With two lockfiles and nothing said, npm's wins.
pm=$(node -p 'try { (require("./package.json").packageManager || "").split("@")[0] } catch (e) { "" }')
if [ -z "$pm" ]; then
  if [ -f pnpm-lock.yaml ]; then pm=pnpm
  elif [ -f package-lock.json ]; then pm=npm
  elif [ -f yarn.lock ]; then pm=yarn
  else pm=npm
  fi
fi

case "$pm" in
  pnpm)
    echo "→ pnpm install"; npx --yes pnpm@latest install --frozen-lockfile
    echo "→ pnpm run build"; npx --yes pnpm@latest run build ;;
  yarn)
    echo "→ yarn install"; npx --yes yarn@1 install --frozen-lockfile
    echo "→ yarn build"; npx --yes yarn@1 run build ;;
  *)
    if [ -f package-lock.json ]; then echo "→ npm ci"; npm ci; else echo "→ npm install"; npm install; fi
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

	done, err := r.Run(ctx, Job{
		Image: image, Stage: stage, WorkMB: buildWorkMB,
		Argv: []string{"/bin/sh", "-c", buildScript}, Dir: "/",
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
	// usual places that has an index.html.
	candidates := outputs
	if req.Static != "" {
		candidates = []string{strings.Trim(path.Clean("/"+req.Static), "/")}
	}
	for _, out := range candidates {
		if page, _ := r.read(done.Work, "/src/"+out+"/index.html"); page == "" {
			continue
		}
		if err := r.Extract(done.Work, "/src/"+out, filepath.Join(dir, "out")); err != nil {
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

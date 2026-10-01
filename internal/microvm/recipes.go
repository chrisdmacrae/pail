package microvm

import "time"

// What follows is how Pail builds and runs things, whatever it runs them
// in: a Firecracker microVM here, or a container in package engine.

// What a build gets.
const (
	NodeImage    = "docker.io/library/node:22-slim"
	BuildVCPUs   = 2
	BuildMemMB   = 2048
	BuildTimeout = 15 * time.Minute

	ContainerBuildTimeout = 30 * time.Minute
	// ContainerPullTimeout is how long fetching an image may take.
	ContainerPullTimeout = 15 * time.Minute
	FunctionBuildTimeout = 15 * time.Minute

	// StopGrace is how long a container has to finish after it's asked to
	// stop.
	StopGrace = 10 * time.Second
)

// BuildScript installs a Node project's dependencies and builds it. The root
// filesystem is read-only, so everything a tool caches goes under /work.
// PAIL_DIR, when set, is the folder of the source the project is in.
const BuildScript = `set -e
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

// Outputs are where builds leave a site, most telling first. public comes
// last: in many projects it holds source files, not the result.
var Outputs = []string{"dist", "build", "out", "_site", ".output/public", "public"}

// FunctionPrelude goes before a language's build script. The root
// filesystem is read-only, so everything a tool caches goes under /work.
const FunctionPrelude = `set -e
cd /work/src
export HOME=/work/home CI=true NO_COLOR=1
mkdir -p "$HOME"
`

//go:build linux

package microvm

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// What a Dockerfile build gets.
const (
	buildahImage          = "quay.io/buildah/stable:latest"
	containerBuildWorkMB  = 16384
	containerBuildTimeout = 30 * time.Minute
	// containerSpareMB is the room a container has to write in its root
	// filesystem, beyond what the image holds.
	containerSpareMB = 1024
)

// containerBuildScript builds a Dockerfile with Buildah and leaves the image
// in /work/image.tar. The microVM is the sandbox, so Buildah needs none of
// its own. Layers stack with overlayfs where the guest kernel has it; where
// it doesn't, each is a full copy, which is slower and no less correct.
//
// A Dockerfile's FROM node:22 means Docker Hub's, as it does to Docker.
const containerBuildScript = `set -e
export HOME=/work/home TMPDIR=/tmp
mkdir -p "$HOME" /work/c /work/var/cache /work/var/tmp
# The root filesystem is read-only; what Buildah keeps under /var goes to
# the work disk.
mount --bind /work/var/cache /var/cache
mount --bind /work/var/tmp /var/tmp
printf 'unqualified-search-registries = ["docker.io"]\nshort-name-mode = "permissive"\n' > /work/registries.conf
export CONTAINERS_REGISTRIES_CONF=/work/registries.conf
driver=vfs
if grep -qw overlay /proc/filesystems; then driver=overlay; fi
b() { buildah --root /work/c/storage --runroot /work/c/run --storage-driver "$driver" "$@"; }
cd /work/src
b bud --isolation chroot --network host --iidfile /work/iid -f "$PAIL_DOCKERFILE" "$PAIL_CONTEXT"
echo "→ exporting the image"
b push -q "$(cat /work/iid)" docker-archive:/work/image.tar
`

// BuildContainer builds a Dockerfile in a throwaway microVM that can reach
// the internet for base images and packages and nothing else, and turns the
// image it makes into a root filesystem a microVM can boot.
func (r *Runner) BuildContainer(ctx context.Context, req ContainerBuild) (Built, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, containerBuildTimeout)
	defer cancel()

	if err := os.MkdirAll(filepath.Join(r.cfg.Dir, "builds"), 0o755); err != nil {
		return Built{}, err
	}
	dir, err := os.MkdirTemp(filepath.Join(r.cfg.Dir, "builds"), "c-")
	if err != nil {
		return Built{}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(err error) (Built, error) {
		cleanup()
		return Built{}, err
	}

	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "src"), 0o755); err != nil {
		return fail(err)
	}
	if err := req.Fill(filepath.Join(stage, "src")); err != nil {
		return fail(err)
	}
	builder, err := r.Image(ctx, buildahImage, log)
	if err != nil {
		return fail(err)
	}

	done, err := r.Run(ctx, Job{
		Image: builder, Stage: stage, WorkMB: containerBuildWorkMB,
		Argv: []string{"/bin/sh", "-c", containerBuildScript}, Dir: "/",
		Env: []string{
			"PAIL_DOCKERFILE=" + path.Join("/work/src", req.Dockerfile),
			"PAIL_CONTEXT=" + path.Join("/work/src", req.Context),
		},
		VCPUs: buildVCPUs, MemMB: buildMemMB, Network: true, Log: log,
	})
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fail(fmt.Errorf("the build ran for %s and was stopped", containerBuildTimeout))
	case err != nil:
		return fail(err)
	case done.ExitCode != 0:
		return fail(fmt.Errorf("the build exited with status %d", done.ExitCode))
	}

	archive := filepath.Join(dir, "image.tar")
	if err := r.dump(done.Work, "/image.tar", archive); err != nil {
		return fail(err)
	}
	os.Remove(done.Work) // the image is out; the rest was scaffolding
	os.RemoveAll(stage)
	built, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		return fail(fmt.Errorf("can't read the image the build made: %w", err))
	}
	img := &Image{Path: filepath.Join(dir, "root.ext4")}
	if err := r.flatten(ctx, built, img, containerSpareMB); err != nil {
		return fail(fmt.Errorf("can't turn the image into a root filesystem: %w", err))
	}
	os.Remove(archive)
	return Built{Image: img, Cleanup: cleanup}, nil
}

// containerPullTimeout is how long fetching an image may take.
const containerPullTimeout = 15 * time.Minute

// PullContainer fetches an image from a registry and turns it into a root
// filesystem a microVM can boot. Nothing in the image runs on this machine:
// its files are unpacked into a folder, and only ever run inside a microVM.
func (r *Runner) PullContainer(ctx context.Context, req ContainerPull) (Built, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, containerPullTimeout)
	defer cancel()

	// This reads the image's manifest; its layers come when they're unpacked.
	pulled, err := crane.Pull(req.Ref, crane.WithContext(ctx), crane.WithPlatform(&v1.Platform{OS: "linux", Architecture: runtime.GOARCH}))
	if err != nil {
		return Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	digest, err := pulled.Digest()
	if err != nil {
		return Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	if req.Unless != "" && digest.String() == req.Unless {
		return Built{Digest: digest.String()}, nil
	}

	if err := os.MkdirAll(filepath.Join(r.cfg.Dir, "builds"), 0o755); err != nil {
		return Built{}, err
	}
	dir, err := os.MkdirTemp(filepath.Join(r.cfg.Dir, "builds"), "p-")
	if err != nil {
		return Built{}, err
	}
	log("pulling " + req.Ref + " (" + digest.Hex[:12] + ")")
	img := &Image{Path: filepath.Join(dir, "root.ext4")}
	if err := r.flatten(ctx, pulled, img, containerSpareMB); err != nil {
		os.RemoveAll(dir)
		if ctx.Err() == context.DeadlineExceeded {
			return Built{}, fmt.Errorf("pulling %s ran for %s and was stopped", req.Ref, containerPullTimeout)
		}
		return Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	return Built{Image: img, Digest: digest.String(), Cleanup: func() { os.RemoveAll(dir) }}, nil
}

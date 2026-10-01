package engine

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

// BuildSite builds a Node project in a throwaway container and returns the
// folder the built site is in.
func (r *Runner) BuildSite(ctx context.Context, req microvm.BuildRequest) (microvm.BuildResult, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, microvm.BuildTimeout)
	defer cancel()

	dir, err := r.buildDir("b-")
	if err != nil {
		return microvm.BuildResult{}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(err error) (microvm.BuildResult, error) {
		cleanup()
		return microvm.BuildResult{}, err
	}

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fail(err)
	}
	if err := req.Fill(src); err != nil {
		return fail(err)
	}
	if _, err := r.pull(ctx, microvm.NodeImage, log); err != nil {
		return fail(err)
	}

	// Where the project is inside the source, for the script and for
	// finding what it built.
	project := strings.Trim(path.Clean("/"+req.Dir), "/")
	var env []string
	if project != "" {
		env = []string{"PAIL_DIR=" + project}
	}
	id, exit, err := r.run(ctx, job{Image: qualify(microvm.NodeImage), Script: microvm.BuildScript, Env: env, Src: src, Log: log})
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fail(fmt.Errorf("the build ran for %s and was stopped", microvm.BuildTimeout))
	case err != nil:
		return fail(err)
	}
	defer r.discard(id)
	os.RemoveAll(src) // the container has its own copy
	if exit != 0 {
		return fail(fmt.Errorf("the build exited with status %d", exit))
	}

	// Find where the site landed: where pail.json says, or the first of the
	// usual places that has an index.html. A build that makes functions too
	// leaves a pail.json of its own there instead, to say what is what.
	candidates := microvm.Outputs
	if req.Static != "" {
		candidates = []string{strings.Trim(path.Clean("/"+req.Static), "/")}
	}
	for _, out := range candidates {
		built := "/work/src/" + path.Join(project, out)
		if !r.api.has(ctx, id, built+"/index.html") && !r.api.has(ctx, id, built+"/pail.json") {
			continue
		}
		files, err := r.api.copyOut(ctx, id, built)
		if err != nil {
			return fail(err)
		}
		err = untar(files, filepath.Join(dir, "out"))
		files.Close()
		if err != nil {
			return fail(err)
		}
		return microvm.BuildResult{Dir: filepath.Join(dir, "out", path.Base(out)), Output: out, Cleanup: cleanup}, nil
	}
	if req.Static != "" {
		return fail(fmt.Errorf("the build finished, but left no index.html in ./%s, where pail.json says the site is", candidates[0]))
	}
	return fail(fmt.Errorf("the build finished, but Pail found no index.html in any of: %s. Say where the site lands with \"static\" in pail.json", strings.Join(microvm.Outputs, ", ")))
}

// A container's image is kept with its deploy as one file: the archive an
// engine saves an image to and loads it from. The file says which image it
// holds, so any engine given it can run the deploy.

// BuildContainer has the engine build a Dockerfile, and returns the image
// it makes as a file.
func (r *Runner) BuildContainer(ctx context.Context, req microvm.ContainerBuild) (microvm.Built, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, microvm.ContainerBuildTimeout)
	defer cancel()

	dir, err := r.buildDir("c-")
	if err != nil {
		return microvm.Built{}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(err error) (microvm.Built, error) {
		cleanup()
		return microvm.Built{}, err
	}

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fail(err)
	}
	if err := req.Fill(src); err != nil {
		return fail(err)
	}

	// The Dockerfile is named by where it is in the context. One that is
	// outside it is sent along under a name of its own.
	folder := filepath.Join(src, filepath.FromSlash(req.Context))
	dockerfile := filepath.Join(src, filepath.FromSlash(req.Dockerfile))
	named, err := filepath.Rel(folder, dockerfile)
	outside := err != nil || named == ".." || strings.HasPrefix(named, ".."+string(filepath.Separator))
	if outside {
		named = ".pail.Dockerfile"
	}
	buildContext := archive(func(w *tar.Writer) error {
		if outside {
			if err := tarFile(w, dockerfile, named); err != nil {
				return err
			}
		}
		return tarDir(w, folder, "")
	})
	ref := r.tag("c", randomHex(12))
	err = r.api.build(ctx, ref, filepath.ToSlash(named), r.labels(), buildContext, log)
	buildContext.Close()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fail(fmt.Errorf("the build ran for %s and was stopped", microvm.ContainerBuildTimeout))
	case err != nil:
		return fail(err)
	}
	os.RemoveAll(src)
	r.use(ref)

	info, err := r.api.image(ctx, ref)
	if err != nil {
		return fail(fmt.Errorf("can't read the image the build made: %w", err))
	}
	img := &microvm.Image{
		Path: filepath.Join(dir, "image.tar"),
		Env:  info.Config.Env, Entrypoint: info.Config.Entrypoint, Cmd: info.Config.Cmd,
		WorkingDir: info.Config.WorkingDir, User: info.Config.User,
	}
	log("→ exporting the image")
	saved, err := r.api.save(ctx, ref)
	if err != nil {
		return fail(fmt.Errorf("can't export the image the build made: %w", err))
	}
	err = writeFile(img.Path, saved)
	saved.Close()
	if err != nil {
		return fail(fmt.Errorf("can't export the image the build made: %w", err))
	}
	return microvm.Built{Image: img, Cleanup: cleanup}, nil
}

// PullContainer fetches an image from a registry into a file. Pail does the
// fetching, not the engine, so the registry is asked what the image is now
// before anything is downloaded.
func (r *Runner) PullContainer(ctx context.Context, req microvm.ContainerPull) (microvm.Built, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, microvm.ContainerPullTimeout)
	defer cancel()

	// This reads the image's manifest; its layers come when they're written.
	pulled, err := crane.Pull(req.Ref, crane.WithContext(ctx), crane.WithPlatform(&v1.Platform{OS: "linux", Architecture: runtime.GOARCH}))
	if err != nil {
		return microvm.Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	digest, err := pulled.Digest()
	if err != nil {
		return microvm.Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	if req.Unless != "" && digest.String() == req.Unless {
		return microvm.Built{Digest: digest.String()}, nil
	}
	cfg, err := pulled.ConfigFile()
	if err != nil {
		return microvm.Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}

	dir, err := r.buildDir("p-")
	if err != nil {
		return microvm.Built{}, err
	}
	log("pulling " + req.Ref + " (" + digest.Hex[:12] + ")")
	tag, err := name.NewTag(r.tag("c", digest.Hex[:24]))
	if err != nil {
		os.RemoveAll(dir)
		return microvm.Built{}, err
	}
	img := &microvm.Image{
		Path: filepath.Join(dir, "image.tar"),
		Env:  cfg.Config.Env, Entrypoint: cfg.Config.Entrypoint, Cmd: cfg.Config.Cmd,
		WorkingDir: cfg.Config.WorkingDir, User: cfg.Config.User,
	}
	if err := tarball.WriteToFile(img.Path, tag, pulled); err != nil {
		os.RemoveAll(dir)
		if ctx.Err() == context.DeadlineExceeded {
			return microvm.Built{}, fmt.Errorf("pulling %s ran for %s and was stopped", req.Ref, microvm.ContainerPullTimeout)
		}
		return microvm.Built{}, fmt.Errorf("can't pull %s: %w", req.Ref, err)
	}
	return microvm.Built{Image: img, Digest: digest.String(), Cleanup: func() { os.RemoveAll(dir) }}, nil
}

// imageIn says which image the file at path holds, and gives it to the
// engine if the engine doesn't have it.
func (r *Runner) imageIn(ctx context.Context, file string) (string, error) {
	r.mu.Lock()
	ref := r.refs[file]
	r.mu.Unlock()
	if ref == "" {
		manifest, err := tarball.LoadManifest(func() (io.ReadCloser, error) { return os.Open(file) })
		if err != nil || len(manifest) == 0 || len(manifest[0].RepoTags) == 0 {
			// A Pail that runs microVMs keeps a root filesystem here instead.
			return "", fmt.Errorf("what this deploy keeps isn't a container image; it was made by a Pail that runs microVMs. Deploy it again")
		}
		ref = manifest[0].RepoTags[0]
		r.mu.Lock()
		r.refs[file] = ref
		r.mu.Unlock()
	}
	r.use(ref)
	if _, err := r.api.image(ctx, ref); err == nil {
		return ref, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := r.api.load(ctx, f); err != nil {
		return "", fmt.Errorf("the engine wouldn't load the image: %w", err)
	}
	return ref, nil
}

// tarFile writes one file to w under a name.
func tarFile(w *tar.Writer, file, name string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func writeFile(file string, r io.Reader) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

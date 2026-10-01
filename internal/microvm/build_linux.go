//go:build linux

package microvm

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// buildWorkMB is how big a site build's work disk is.
const buildWorkMB = 8192

// BuildSite builds a Node project in a throwaway microVM that can reach the
// internet for dependencies and nothing else, and returns the folder the
// built site is in.
func (r *Runner) BuildSite(ctx context.Context, req BuildRequest) (BuildResult, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, BuildTimeout)
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
	image, err := r.Image(ctx, NodeImage, log)
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
		Argv: []string{"/bin/sh", "-c", BuildScript}, Env: env, Dir: "/",
		VCPUs: BuildVCPUs, MemMB: BuildMemMB, Network: true, Log: log,
	})
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fail(fmt.Errorf("the build ran for %s and was stopped", BuildTimeout))
	case err != nil:
		return fail(err)
	case done.ExitCode != 0:
		return fail(fmt.Errorf("the build exited with status %d", done.ExitCode))
	}

	// Find where the site landed: where pail.json says, or the first of the
	// usual places that has an index.html. A build that makes functions too
	// leaves a pail.json of its own there instead, to say what is what.
	candidates := Outputs
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
	return fail(fmt.Errorf("the build finished, but Pail found no index.html in any of: %s. Say where the site lands with \"static\" in pail.json", strings.Join(Outputs, ", ")))
}

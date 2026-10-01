// Package engine runs builds, containers and functions in the containers of
// a container engine, Docker or Podman, where there is no KVM for microVMs:
// a laptop, mostly. It does what package microvm does, with the engine's
// isolation in place of a virtual machine's.
//
// Pail reaches the engine over its socket and never shares a disk with it,
// so Pail can itself be one of the engine's containers. What it starts are
// its siblings, on a network they share with it.
package engine

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

// Config says where the engine is and what Pail may use of it.
type Config struct {
	// Socket is the engine's API socket.
	Socket string
	// Network is the engine's network Pail's containers join. Pail has to be
	// able to reach addresses on it, as it can when it is on it too. It also
	// names this Pail's things among the engine's others.
	Network string
	// Dir is local disk for what builds leave.
	Dir string
}

// What the engine is told about the things Pail makes.
const (
	// labelInstance marks everything a Pail makes with its Network.
	labelInstance = "sh.pail.instance"
	// labelData and labelMarker are on a data volume: which Pail's disk
	// knows it, and the file there that stands for it.
	labelData   = "sh.pail.data"
	labelMarker = "sh.pail.marker"
)

// volumeSweep is how often the data volumes of removed pails are looked for.
const volumeSweep = 5 * time.Minute

// imageSweep is how long after Pail starts the images it left last time,
// and hasn't asked for since, are removed.
const imageSweep = 15 * time.Minute

// Runner runs containers on one engine.
type Runner struct {
	cfg Config
	log *slog.Logger
	api *api

	mu sync.Mutex
	// used are the images asked for since Pail started.
	used map[string]bool
	// refs remembers which image a file on disk holds.
	refs map[string]string
	// making lets one function's image be made once, however many copies
	// are started at once.
	making map[string]*sync.Mutex

	selfOnce sync.Once
	self     string
	selfID   string
	selfErr  error
}

var _ microvm.Machines = (*Runner)(nil)

func New(cfg Config, logger *slog.Logger) *Runner {
	cfg.Socket = strings.TrimPrefix(cfg.Socket, "unix://")
	return &Runner{
		cfg: cfg, log: logger, api: newAPI(cfg.Socket),
		used: map[string]bool{}, refs: map[string]string{}, making: map[string]*sync.Mutex{},
	}
}

// Sandbox is what this runner runs things in, for messages.
func (r *Runner) Sandbox() string { return "container" }

// Available says whether there is an engine to run containers on, and if
// not, why, in words fit to show someone.
func (r *Runner) Available() (bool, string) {
	if r == nil {
		return false, "it has no container engine"
	}
	if runtime.GOOS != "linux" {
		// Pail puts a copy of itself in each function's container.
		return false, "running in containers needs Pail itself to run on Linux, as it does in its own container, and this Pail runs on " + runtime.GOOS
	}
	if _, err := os.Stat(r.cfg.Socket); err != nil {
		return false, "there is no container engine's socket at " + r.cfg.Socket + " (set PAIL_CONTAINER_SOCKET)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := r.api.do(ctx, "GET", "/_ping", nil, nil, "")
	if err != nil {
		return false, "the container engine at " + r.cfg.Socket + " doesn't answer"
	}
	resp.Body.Close()
	return true, ""
}

// Kind names the engine: Docker or Podman.
func (r *Runner) Kind() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var version struct {
		Components []struct {
			Name string `json:"Name"`
		} `json:"Components"`
	}
	if err := r.api.call(ctx, "GET", "/version", nil, nil, &version); err != nil {
		return "a container engine"
	}
	for _, c := range version.Components {
		if strings.Contains(strings.ToLower(c.Name), "podman") {
			return "Podman"
		}
	}
	return "Docker"
}

// Prepare makes the engine ready for this Pail: its network is there, and
// what an earlier run left behind is gone. Call it once, before anything is
// started.
func (r *Runner) Prepare(ctx context.Context) error {
	err := r.api.call(ctx, "GET", "/networks/"+r.cfg.Network, nil, nil, nil)
	if notFound(err) {
		err = r.api.call(ctx, "POST", "/networks/create", nil, map[string]any{"Name": r.cfg.Network, "Labels": r.labels()}, nil)
	}
	if err != nil {
		return fmt.Errorf("the engine's %s network: %w", r.cfg.Network, err)
	}

	// No container outlives the Pail that started it: these were left when
	// one stopped without the chance to tidy.
	var left []struct {
		ID string `json:"Id"`
	}
	query := filter(labelInstance + "=" + r.cfg.Network)
	query.Set("all", "1")
	if err := r.api.call(ctx, "GET", "/containers/json", query, nil, &left); err != nil {
		return err
	}
	for _, c := range left {
		if err := r.api.remove(ctx, c.ID); err != nil {
			r.log.Warn("remove a container left from last time", "container", c.ID, "err", err)
		}
	}

	// A removed pail's data goes with it: now, for those removed while Pail
	// was away, and every so often from here on.
	r.sweepVolumes(ctx)
	go func() {
		for range time.Tick(volumeSweep) {
			sweep, cancel := context.WithTimeout(context.Background(), time.Minute)
			r.sweepVolumes(sweep)
			cancel()
		}
	}()
	if images := r.images(ctx); len(images) > 0 {
		time.AfterFunc(imageSweep, func() { r.sweepImages(images) })
	}
	return nil
}

func (r *Runner) labels() map[string]string {
	return map[string]string{labelInstance: r.cfg.Network}
}

// tag names an image of this Pail's. The registry in it is one nobody has,
// so no engine goes looking for the image anywhere but on itself.
func (r *Runner) tag(kind, id string) string {
	return "pail.local/" + r.cfg.Network + "/" + kind + ":" + id
}

// use notes that an image is wanted, so the sweep leaves it.
func (r *Runner) use(ref string) {
	r.mu.Lock()
	r.used[ref] = true
	r.mu.Unlock()
}

// images lists the images this Pail has made on the engine.
func (r *Runner) images(ctx context.Context) []string {
	var all []struct {
		RepoTags []string `json:"RepoTags"`
	}
	if err := r.api.call(ctx, "GET", "/images/json", nil, nil, &all); err != nil {
		return nil
	}
	var mine []string
	for _, image := range all {
		for _, tag := range image.RepoTags {
			if strings.HasPrefix(tag, "pail.local/"+r.cfg.Network+"/") {
				mine = append(mine, tag)
			}
		}
	}
	return mine
}

// sweepImages removes the images that were there when Pail started and that
// nothing has asked for since: those of deploys long replaced. One that is
// wanted after all is made again from what its deploy keeps.
func (r *Runner) sweepImages(images []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, ref := range images {
		r.mu.Lock()
		used := r.used[ref]
		r.mu.Unlock()
		if !used {
			r.api.removeImage(ctx, ref) // refused if a container still has it
		}
	}
}

// pull fetches an image unless the engine has it, and says what it knows of
// it.
func (r *Runner) pull(ctx context.Context, ref string, log func(string)) (imageInfo, error) {
	ref = qualify(ref)
	if info, err := r.api.image(ctx, ref); err == nil {
		return info, nil
	}
	log("pulling " + ref)
	if err := r.api.pull(ctx, ref); err != nil {
		return imageInfo{}, fmt.Errorf("can't pull %s: %w", ref, err)
	}
	return r.api.image(ctx, ref)
}

// qualify spells an image's name out in full, registry and all: python:3
// is docker.io/library/python:3. Docker assumes as much; Podman doesn't.
func qualify(ref string) string {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return ref
	}
	full := parsed.Name()
	if rest, ok := strings.CutPrefix(full, name.DefaultRegistry+"/"); ok {
		full = "docker.io/" + rest
	}
	return full
}

// executable is this binary, which a function's container runs as its
// agent, and a name for this build of it.
func (r *Runner) executable() (string, string, error) {
	r.selfOnce.Do(func() {
		self, err := os.Executable()
		if err != nil {
			r.selfErr = err
			return
		}
		sum, err := hashFile(self)
		if err != nil {
			r.selfErr = err
			return
		}
		r.self, r.selfID = self, sum
	})
	return r.self, r.selfID, r.selfErr
}

func hashFile(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// buildDir makes a folder for one build's files.
func (r *Runner) buildDir(prefix string) (string, error) {
	builds := filepath.Join(r.cfg.Dir, "builds")
	if err := os.MkdirAll(builds, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(builds, prefix)
}

// job is one command run to its end in a throwaway container.
type job struct {
	// Image is the image the command runs in.
	Image string
	// Script is run by the image's shell.
	Script string
	Env    []string
	// Src is a folder on this machine that becomes /work/src.
	Src string
	Log func(line string)
}

// run runs a job and waits for it. The container is left for the caller to
// take the job's files from, then remove. Cancelling ctx stops the job.
func (r *Runner) run(ctx context.Context, j job) (id string, exit int, err error) {
	id, err = r.api.create(ctx, "", containerConfig{
		Image: j.Image, Entrypoint: []string{"/bin/sh", "-c", j.Script}, Env: j.Env, WorkingDir: "/",
		User: "0:0", Labels: r.labels(),
		HostConfig: hostConfig{Memory: microvm.BuildMemMB << 20, NanoCPUs: microvm.BuildVCPUs * 1e9},
	})
	if err != nil {
		return "", 0, err
	}
	src := archive(func(w *tar.Writer) error { return tarDir(w, j.Src, "work/src") })
	err = r.api.copyIn(ctx, id, "/", src)
	src.Close()
	if err == nil {
		err = r.api.start(ctx, id)
	}
	if err != nil {
		r.discard(id)
		return "", 0, err
	}
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		r.api.logs(ctx, id, j.Log)
	}()
	exit, err = r.api.wait(ctx, id)
	if err != nil {
		r.discard(id)
		<-printed
		return "", 0, err
	}
	<-printed
	return id, exit, nil
}

// discard removes a container, whatever became of the request that made it.
func (r *Runner) discard(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.api.remove(ctx, id); err != nil {
		r.log.Warn("remove a container", "container", id, "err", err)
	}
}

// address is where a container is on Pail's network.
func (r *Runner) address(ctx context.Context, id string) (string, error) {
	st, err := r.api.inspect(ctx, id)
	if err != nil {
		return "", err
	}
	if n, ok := st.NetworkSettings.Networks[r.cfg.Network]; ok && n.IPAddress != "" {
		return n.IPAddress, nil
	}
	return "", fmt.Errorf("the engine gave the container no address on its %s network", r.cfg.Network)
}

// ended waits for a container to stop, however long that takes and
// whatever becomes of the connection to the engine meanwhile.
func (r *Runner) ended(id string) {
	for {
		if _, err := r.api.wait(context.Background(), id); err == nil || notFound(err) {
			return
		}
		time.Sleep(2 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		st, err := r.api.inspect(ctx, id)
		cancel()
		if notFound(err) || (err == nil && !st.State.Running) {
			return
		}
	}
}

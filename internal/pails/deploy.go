package pails

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/microvm"
)

// maxFiles caps how many files one deploy may hold.
const maxFiles = 50_000

// Upload is an archive on its way to becoming a deploy.
type Upload struct {
	// Path is the archive on local disk. The deploy deletes it when done.
	Path string
	// Source is where it came from: "cli" or "upload".
	Source string
	// Label says what this deploy is, for the history: "pail up from 10.0.0.23".
	Label string
	// URL is the pail's address as the uploader reaches it, for the log.
	URL string
}

// userError is a deploy failure the person can fix. Its text goes in the log
// and the API as written.
type userError struct{ msg string }

func (e userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return userError{fmt.Sprintf(format, args...)}
}

var errRemoved = userError{"The pail was removed while this deploy was running."}

// job is the part of a deploy that depends on where its files come from.
type job struct {
	// url is the pail's address as the person who asked reaches it.
	url string
	// fill puts the deploy's files in its folder, fills in d's file count
	// and size, and returns what the deploy serves.
	fill func(ctx context.Context, e *entry, d *Deploy, lg *Log) (*Manifest, error)
	// done, if set, runs when the deploy is over, however it went.
	done func()
}

// StartDeploy records a new deploy of name, creating the pail if this is its
// first, and runs it in the background. Follow it with Log.
func (s *Service) StartDeploy(name string, up Upload) (Deploy, error) {
	if !ValidName(name) {
		os.Remove(up.Path)
		return Deploy{}, ErrBadName
	}
	format, err := sniffArchive(up.Path)
	if err != nil {
		os.Remove(up.Path)
		return Deploy{}, err
	}

	s.mu.Lock()
	if s.removing[name] {
		s.mu.Unlock()
		os.Remove(up.Path)
		return Deploy{}, ErrBusy
	}
	now := time.Now().UTC()
	e := s.pails[name]
	if e == nil {
		e = s.entry(name)
		e.rec = record{Name: name, CreatedAt: now}
	}
	d, lg := s.begin(e, up.Source, up.Label)
	s.mu.Unlock()

	s.launch(e, d, lg, job{
		url:  up.URL,
		done: func() { os.Remove(up.Path) },
		fill: func(ctx context.Context, e *entry, d *Deploy, lg *Log) (*Manifest, error) {
			return s.unpack(ctx, e.name, d, up.Path, format, lg)
		},
	})
	return d, nil
}

// Redeploy makes a new deploy from the files of the pail's latest good one
// and puts it live, as a fresh deploy of the same source would be. url is
// the pail's address, for the log.
func (s *Service) Redeploy(name, url string) (Deploy, error) {
	s.mu.Lock()
	e := s.pails[name]
	if e == nil {
		s.mu.Unlock()
		return Deploy{}, ErrNoPail
	}
	src := e.latestGood()
	if src == nil {
		s.mu.Unlock()
		return Deploy{}, ErrNoSource
	}
	d, lg := s.begin(e, src.Source, "redeploy of "+src.ID)
	s.mu.Unlock()

	s.launch(e, d, lg, job{url: url, fill: s.recopy})
	return d, nil
}

// begin adds a building deploy to the pail's history. The caller holds mu.
func (s *Service) begin(e *entry, source, label string) (Deploy, *Log) {
	d := Deploy{ID: newDeployID(), State: DeployBuilding, Source: source, Label: label, CreatedAt: time.Now().UTC()}
	for e.hasDeploy(d.ID) {
		d.ID = newDeployID()
	}
	lg := newLog()
	e.deploys = append([]Deploy{d}, e.deploys...)
	e.logs[d.ID] = lg
	return d, lg
}

func (s *Service) launch(e *entry, d Deploy, lg *Log, j job) {
	s.deploys.Add(1)
	go func() {
		defer s.deploys.Done()
		s.run(e, d, lg, j)
	}()
}

// latestGood is the newest deploy that finished, or nil. The caller holds mu.
func (e *entry) latestGood() *Deploy {
	for i := range e.deploys {
		if e.deploys[i].State == DeployOK {
			d := e.deploys[i]
			return &d
		}
	}
	return nil
}

// recopy fills a deploy with a copy of the latest good deploy's files.
func (s *Service) recopy(ctx context.Context, e *entry, d *Deploy, lg *Log) (*Manifest, error) {
	s.mu.RLock()
	src := e.latestGood()
	s.mu.RUnlock()
	if src == nil {
		return nil, userErrorf("There's no finished deploy left to redeploy. Run pail up to send the files again.")
	}
	man := &Manifest{}
	if err := s.readJSON(ctx, manifestKey(e.name, src.ID), man); err != nil {
		return nil, err
	}
	from, to := deployFiles(e.name, src.ID), deployFiles(e.name, d.ID)
	keys, err := s.store.List(ctx, from)
	if err != nil {
		return nil, err
	}
	lg.add("", "copying %d %s from %s", len(keys), plural(len(keys), "file"), src.ID)
	for _, key := range keys {
		if err := s.store.Copy(ctx, key, to+strings.TrimPrefix(key, from)); err != nil {
			return nil, err
		}
	}
	d.Files, d.Bytes = src.Files, src.Bytes
	// Its containers run again from the root filesystems already built. One
	// that runs a registry's image is asked for afresh, so a redeploy picks
	// up a tag that has moved.
	containers := map[string]Container{}
	for c, cc := range man.Containers {
		if cc.Image != "" {
			if err := s.canRun(); err != nil {
				return nil, err
			}
			pulled, err := s.pullContainer(ctx, e.name, d.ID, c, cc, src.ID, &cc, lg)
			if err != nil {
				return nil, err
			}
			cc = pulled
		} else if err := s.shareRootfs(ctx, e.name, src.ID, d.ID, c); err != nil {
			return nil, err
		}
		containers[c] = cc
	}
	if len(containers) > 0 {
		// The manifest read above may be the one being served; leave it be.
		next := *man
		next.Containers = containers
		man = &next
	}
	// Its functions run again from what was built and snapshotted then.
	for f, fn := range man.Functions {
		if err := s.shareFunction(ctx, e.name, src.ID, d.ID, f, fn); err != nil {
			return nil, err
		}
	}
	return man, nil
}

func (e *entry) hasDeploy(id string) bool {
	for _, d := range e.deploys {
		if d.ID == id {
			return true
		}
	}
	return false
}

// run takes one deploy from its files to live. Nothing a request can see
// changes until cutover; any failure before it leaves the previous deploy
// serving.
func (s *Service) run(e *entry, d Deploy, lg *Log, j job) {
	e.work.Lock()
	defer e.work.Unlock()
	if j.done != nil {
		defer j.done()
	}
	ctx := context.Background()

	lg.add("step", "→ %s (%s)", d.Label, d.ID)
	err := s.build(ctx, e, &d, lg, j)

	now := time.Now().UTC()
	d.FinishedAt = &now
	if err == nil {
		d.State = DeployOK
		lg.add("ok", "✓ live at %s", j.url)
	} else {
		d.State = DeployFailed
		d.Error = err.Error()
		var ue userError
		if !errors.As(err, &ue) {
			s.log.Error("deploy failed", "pail", e.name, "deploy", d.ID, "err", err)
			d.Error = "Pail couldn't store this deploy: " + err.Error()
		}
		lg.add("error", "error: %s", d.Error)
		if serving := s.serving(e); serving != "" {
			lg.add("error", "✗ deploy failed · still serving %s", serving)
		} else {
			lg.add("error", "✗ deploy failed · nothing is live yet")
		}
		if err := s.store.DeletePrefix(ctx, deployFiles(e.name, d.ID)); err != nil {
			s.log.Error("clean up failed deploy", "pail", e.name, "deploy", d.ID, "err", err)
		}
		s.dropRootfs(ctx, e.name, d.ID)
	}

	s.mu.Lock()
	removed := e.removed
	for i := range e.deploys {
		if e.deploys[i].ID == d.ID {
			e.deploys[i] = d
		}
	}
	s.mu.Unlock()

	if !removed {
		lines := lg.encode()
		if err := s.store.Put(ctx, logKey(e.name, d.ID), bytes.NewReader(lines), int64(len(lines)), "application/x-ndjson"); err != nil {
			s.log.Error("save deploy log", "pail", e.name, "deploy", d.ID, "err", err)
		}
		if err := s.writeJSON(ctx, deployKey(e.name, d.ID), d); err != nil {
			s.log.Error("save deploy", "pail", e.name, "deploy", d.ID, "err", err)
		}
	}

	// The log closes only once the stored copy and the final state are
	// there for readers to fall back on.
	lg.close()
	s.mu.Lock()
	delete(e.logs, d.ID)
	s.mu.Unlock()

	if !removed {
		s.prune(ctx, e)
	}
}

func (s *Service) serving(e *entry) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return e.rec.Serving
}

// build fills the deploy's folder, checks it and cuts over.
func (s *Service) build(ctx context.Context, e *entry, d *Deploy, lg *Log, j job) error {
	// The pail and the deploy exist in storage from here, so a restart
	// mid-deploy finds them and settles the deploy as failed.
	if s.serving(e) == "" {
		if err := s.updateRecord(ctx, e, nil, func(*record) {}); err != nil {
			return err
		}
	}
	if err := s.writeJSON(ctx, deployKey(e.name, d.ID), d); err != nil {
		return err
	}

	man, err := j.fill(ctx, e, d, lg)
	if err != nil {
		return err
	}
	if err := s.writeJSON(ctx, manifestKey(e.name, d.ID), man); err != nil {
		return err
	}

	// Cutover: containers up and answering, then one write moves the live
	// pointer.
	return s.goLive(ctx, e, d.ID, man, lg)
}

// plan is what the first pass over an archive decides.
type plan struct {
	strip    string // folder every file sits in, dropped from names
	names    map[string]bool
	pailJSON []byte
	// packageJSON is the project's package.json, if it has one at the top.
	packageJSON []byte
}

// needsBuild reports whether a project has to be built before it can be
// served: its package.json has a build script.
func needsBuild(packageJSON []byte) bool {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	return json.Unmarshal(packageJSON, &pkg) == nil && pkg.Scripts["build"] != ""
}

// unpack copies the archive's files into the deploy's own folder and returns
// the manifest of what it serves.
func (s *Service) unpack(ctx context.Context, name string, d *Deploy, archive string, format archiveFormat, lg *Log) (*Manifest, error) {
	p, err := s.survey(archive, format)
	if err != nil {
		return nil, err
	}

	cfg := deployConfig{files: true}
	found := "index.html"
	if p.pailJSON != nil {
		found = "pail.json"
		if cfg, err = parsePailJSON(p.pailJSON); err != nil {
			return nil, err
		}
	}
	// A project with server code is built by its Dockerfiles and functions,
	// not as a site.
	if needsBuild(p.packageJSON) && len(cfg.containers)+len(cfg.functions) == 0 {
		return s.buildAndStore(ctx, name, d, archive, format, p, cfg, lg)
	}
	static := "./" + strings.TrimSuffix(cfg.root, "/")
	switch {
	case p.pailJSON == nil && !p.names["index.html"]:
		return nil, userErrorf("No index.html at the top of the upload. Point Pail at the folder that has it.")
	case cfg.fallback != "" && !p.names[cfg.root+cfg.fallback]:
		return nil, userErrorf("pail.json falls back to %s, but %s has no such file.", cfg.fallback, static)
	}
	plans := map[string]functionPlan{}
	if len(cfg.containers)+len(cfg.functions) > 0 {
		// Refused before anything is stored or built.
		if err := s.canRun(); err != nil {
			return nil, err
		}
		for _, f := range cfg.functionNames() {
			fc := cfg.functions[f]
			if most := s.maxFunctionMem >> 20; most > 0 && int64(fc.MemoryMB) > most {
				return nil, userErrorf("pail.json: %s asks for %s of memory, and this Pail gives a function at most %s. Ask for less, or raise PAIL_MAX_FUNCTION_MEMORY on the server.",
					f, config.FormatSize(int64(fc.MemoryMB)<<20), config.FormatSize(s.maxFunctionMem))
			}
			if plans[f], err = planFunction(f, fc, p.names); err != nil {
				return nil, err
			}
		}
		for _, c := range cfg.names() {
			if most := s.maxContainerMem >> 20; most > 0 && int64(cfg.containers[c].MemoryMB) > most {
				return nil, userErrorf("pail.json: %s asks for %s of memory, and this Pail gives a container at most %s. Ask for less, or raise PAIL_MAX_CONTAINER_MEMORY on the server.",
					c, config.FormatSize(int64(cfg.containers[c].MemoryMB)<<20), config.FormatSize(s.maxContainerMem))
			}
			if file := cfg.containers[c].dockerfile; file != "" && !p.names[file] {
				return nil, userErrorf("pail.json builds %s from ./%s, and the upload has no such file.", c, file)
			}
		}
	}

	lg.add("", "unpacking %d %s · found %s", len(p.names), plural(len(p.names), "file"), found)

	man := &Manifest{Root: cfg.root, Fallback: cfg.fallback, Files: map[string]File{}, Routes: cfg.routes}
	prefix := deployFiles(name, d.ID)
	err = walkArchive(archive, format, func(raw string, size int64, r io.Reader) error {
		file, skip, err := cleanName(raw)
		if err != nil || skip {
			return err
		}
		file = strings.TrimPrefix(file, p.strip)
		stored, err := s.storeFile(ctx, prefix+file, r, size, d)
		if err != nil {
			return err
		}
		if served, ok := strings.CutPrefix(file, cfg.root); ok && cfg.files && file != "pail.json" {
			man.Files[served] = stored
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cfg.files && len(man.Files) == 0 {
		return nil, userErrorf("pail.json points static at %s, but the upload has no files there.", static)
	}
	if len(cfg.containers) > 0 {
		if err := s.buildContainers(ctx, name, d, archive, format, p, cfg, man, lg); err != nil {
			return nil, err
		}
	}
	if len(cfg.functions) > 0 {
		if err := s.buildFunctions(ctx, name, d, archive, format, p, cfg, plans, man, lg); err != nil {
			return nil, err
		}
	}
	return man, nil
}

// buildContainers builds each of a deploy's Dockerfiles in a throwaway
// microVM and keeps the root filesystem each one makes with the deploy.
func (s *Service) buildContainers(ctx context.Context, name string, d *Deploy, archive string, format archiveFormat, p plan, cfg deployConfig, man *Manifest, lg *Log) error {
	man.Containers = map[string]Container{}
	for _, c := range cfg.names() {
		cc := cfg.containers[c]
		if cc.Image != "" {
			// The image the pail is serving now, if it runs this one too.
			prevID, prev := s.servingContainer(ctx, name, c)
			pulled, err := s.pullContainer(ctx, name, d.ID, c, cc.Container, prevID, prev, lg)
			if err != nil {
				return err
			}
			man.Containers[c] = pulled
			continue
		}
		lg.add("step", "→ building %s from ./%s in a microVM", c, cc.dockerfile)
		built, err := s.builder.BuildContainer(ctx, microvm.ContainerBuild{
			Fill:       extractTo(archive, format, p.strip),
			Dockerfile: cc.dockerfile,
			Context:    cc.context,
			Log:        func(line string) { lg.add("", "%s", line) },
		})
		if err != nil {
			var ue userError
			if errors.As(err, &ue) {
				return err
			}
			return userErrorf("%s didn't build: %v.", c, err)
		}
		img := built.Image
		cc.Entrypoint, cc.Cmd = img.Entrypoint, img.Cmd
		cc.ImageEnv, cc.WorkingDir, cc.User = img.Env, img.WorkingDir, img.User
		if len(cc.argv()) == 0 {
			built.Cleanup()
			return userErrorf("%s's Dockerfile has no CMD or ENTRYPOINT, so Pail doesn't know what to run. Add one, or say what with \"command\" in pail.json.", c)
		}
		lg.add("", "built %s · keeping its root filesystem with the deploy", c)
		err = s.keepRootfs(ctx, name, d.ID, c, img.Path)
		built.Cleanup()
		if err != nil {
			return err
		}
		man.Containers[c] = cc.Container
	}
	return nil
}

// servingContainer is a container of the deploy a pail is serving, and that
// deploy's ID, or nil.
func (s *Service) servingContainer(ctx context.Context, name, c string) (string, *Container) {
	s.mu.RLock()
	e := s.pails[name]
	s.mu.RUnlock()
	if e == nil {
		return "", nil
	}
	id := s.serving(e)
	if id == "" {
		return "", nil
	}
	man, err := s.manifestOf(ctx, e, id)
	if err != nil {
		return "", nil
	}
	if prev, ok := man.Containers[c]; ok {
		return id, &prev
	}
	return "", nil
}

// pullContainer gives a deploy the root filesystem of a container that runs
// an image from a registry. The registry is asked what the image is now, so
// a tag that has moved is followed; when it is what an earlier deploy
// (prevID) already pulled, that deploy's root filesystem is used again.
func (s *Service) pullContainer(ctx context.Context, name, id, c string, cc Container, prevID string, prev *Container, lg *Log) (Container, error) {
	lg.add("step", "→ pulling %s for %s", cc.Image, c)
	req := microvm.ContainerPull{Ref: cc.Image, Log: func(line string) { lg.add("", "%s", line) }}
	if prev != nil && prev.Image == cc.Image {
		req.Unless = prev.Digest
	}
	for {
		built, err := s.builder.PullContainer(ctx, req)
		if err != nil {
			return cc, userErrorf("%s's image didn't arrive: %v.", c, err)
		}
		cc.Digest = built.Digest
		if built.Image == nil {
			if err := s.shareRootfs(ctx, name, prevID, id, c); err != nil {
				// What the earlier deploy kept is gone: fetch it after all.
				req.Unless = ""
				continue
			}
			cc.Entrypoint, cc.Cmd = prev.Entrypoint, prev.Cmd
			cc.ImageEnv, cc.WorkingDir, cc.User = prev.ImageEnv, prev.WorkingDir, prev.User
			lg.add("", "%s hasn't changed since %s · using the root filesystem from then", cc.Image, prevID)
			return cc, nil
		}
		img := built.Image
		cc.Entrypoint, cc.Cmd = img.Entrypoint, img.Cmd
		cc.ImageEnv, cc.WorkingDir, cc.User = img.Env, img.WorkingDir, img.User
		if len(cc.argv()) == 0 {
			built.Cleanup()
			return cc, userErrorf("%s has no CMD or ENTRYPOINT, so Pail doesn't know what to run. Say what with \"command\" in pail.json.", cc.Image)
		}
		lg.add("", "pulled %s · keeping its root filesystem with the deploy", c)
		err = s.keepRootfs(ctx, name, id, c, img.Path)
		built.Cleanup()
		return cc, err
	}
}

// shareRootfs gives one deploy the root filesystem another already keeps.
func (s *Service) shareRootfs(ctx context.Context, name, from, to, c string) error {
	if err := s.store.Copy(ctx, rootfsKey(name, from, c), rootfsKey(name, to, c)); err != nil {
		return err
	}
	if s.dir != "" {
		local := s.rootfsFile(name, to, c)
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err == nil {
			os.Link(s.rootfsFile(name, from, c), local) // if it can't, it is fetched from storage
		}
	}
	return nil
}

// dropRootfs deletes what was kept for a deploy's containers and functions.
func (s *Service) dropRootfs(ctx context.Context, name, id string) {
	if s.dir != "" {
		os.RemoveAll(filepath.Join(s.rootfsDir(name), id))
		os.RemoveAll(filepath.Join(s.functionsDir(name), id))
	}
	for _, kind := range []string{"rootfs.", "fn."} {
		if err := s.store.DeletePrefix(ctx, deployMeta(name, id)+kind); err != nil {
			s.log.Error("delete root filesystems", "pail", name, "deploy", id, "err", err)
		}
	}
}

// extractTo returns a function that unpacks an archive's files into a
// folder on local disk, for a build to work on. Files that may be run stay
// that way.
func extractTo(archive string, format archiveFormat, strip string) func(dir string) error {
	return func(dir string) error {
		return walkArchiveModes(archive, format, func(raw string, _ int64, mode fs.FileMode, r io.Reader) error {
			file, skip, err := cleanName(raw)
			if err != nil || skip {
				return err
			}
			target := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(file, strip)))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			perm := fs.FileMode(0o644)
			if mode&0o111 != 0 {
				perm = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, r); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		})
	}
}

// storeFile puts one of a deploy's files in storage, counting it.
func (s *Service) storeFile(ctx context.Context, key string, r io.Reader, size int64, d *Deploy) (File, error) {
	ctype := contentType(key)
	hash := sha256.New()
	if err := s.store.Put(ctx, key, io.TeeReader(r, hash), size, ctype); err != nil {
		return File{}, err
	}
	d.Files++
	d.Bytes += size
	return File{Size: size, ETag: hex.EncodeToString(hash.Sum(nil)[:12]), Type: ctype}, nil
}

// buildAndStore is for a project that has to be built first. The source is
// built in a throwaway microVM, and what the build leaves is what the deploy
// stores and serves.
func (s *Service) buildAndStore(ctx context.Context, name string, d *Deploy, archive string, format archiveFormat, p plan, cfg deployConfig, lg *Log) (*Manifest, error) {
	if ok, why := s.CanBuild(); !ok {
		return nil, userErrorf("This project needs a build, and this Pail can’t run one: %s. Build it yourself and deploy the result with pail up ./dist.", why)
	}
	lg.add("", "unpacking %d %s · found package.json with a build script", len(p.names), plural(len(p.names), "file"))
	lg.add("step", "→ building in a microVM")

	built, err := s.builder.BuildSite(ctx, microvm.BuildRequest{
		// With a build, pail.json's static is where the result lands.
		Static: strings.TrimSuffix(cfg.root, "/"),
		Log:    func(line string) { lg.add("", "%s", line) },
		Fill:   extractTo(archive, format, p.strip),
	})
	if err != nil {
		var ue userError
		if errors.As(err, &ue) {
			return nil, err
		}
		return nil, userErrorf("The build didn’t finish: %v.", err)
	}
	defer built.Cleanup()

	man := &Manifest{Fallback: cfg.fallback, Files: map[string]File{}}
	prefix := deployFiles(name, d.ID)
	err = filepath.WalkDir(built.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(built.Dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		file := filepath.ToSlash(rel)
		stored, err := s.storeFile(ctx, prefix+file, f, info.Size(), d)
		if err != nil {
			return err
		}
		man.Files[file] = stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cfg.fallback != "" && man.Files[cfg.fallback] == (File{}) {
		return nil, userErrorf("pail.json falls back to %s, but the build left no such file in ./%s.", cfg.fallback, built.Output)
	}
	lg.add("", "built ./%s · %d %s", built.Output, len(man.Files), plural(len(man.Files), "file"))
	return man, nil
}

// survey reads the archive once without storing anything: it enforces the
// limits, finds a wrapping folder to drop and picks out pail.json.
func (s *Service) survey(archive string, format archiveFormat) (plan, error) {
	var (
		names    []string
		total    int64
		pailJSON = map[string][]byte{}
	)
	err := walkArchive(archive, format, func(raw string, size int64, r io.Reader) error {
		file, skip, err := cleanName(raw)
		if err != nil || skip {
			return err
		}
		names = append(names, file)
		total += size
		if len(names) > maxFiles {
			return userErrorf("The upload has more than %d files. Point Pail at the built site, not the whole project.", maxFiles)
		}
		if s.maxUnpackedSize > 0 && total > s.maxUnpackedSize {
			return userErrorf("The upload unpacks to more than this Pail takes. Raise PAIL_MAX_UPLOAD_SIZE on the server to send more.")
		}
		// The two files that say what a project is, at the top or one folder in.
		if base := path.Base(file); (base == "pail.json" || base == "package.json") && strings.Count(file, "/") <= 1 {
			b, err := io.ReadAll(io.LimitReader(r, 1<<20))
			if err != nil {
				return userErrorf("%s can't be read: %v.", base, err)
			}
			pailJSON[file] = b
		}
		return nil
	})
	if err != nil {
		return plan{}, err
	}
	if len(names) == 0 {
		return plan{}, userErrorf("The upload is empty. Point Pail at the folder with your site in it.")
	}

	p := plan{names: map[string]bool{}}
	// A zip of a folder holds that folder, not its contents. If nothing at
	// the top says "site" and everything shares one folder, look inside it.
	top := map[string]bool{}
	for _, n := range names {
		first, _, nested := strings.Cut(n, "/")
		if !nested {
			top = nil
			break
		}
		top[first] = true
	}
	if len(top) == 1 {
		for folder := range top {
			p.strip = folder + "/"
		}
	}
	for _, n := range names {
		p.names[strings.TrimPrefix(n, p.strip)] = true
	}
	p.pailJSON = pailJSON[p.strip+"pail.json"]
	p.packageJSON = pailJSON[p.strip+"package.json"]
	return p, nil
}

// prune deletes deploys past the limit, oldest first, never the one being
// served. Good and failed deploys are counted apart, each against the limit:
// a failed deploy holds no files and can't be rolled back to, so it never
// takes the place of one that can.
func (s *Service) prune(ctx context.Context, e *entry) {
	s.mu.Lock()
	good, failed := 0, 0
	for _, d := range e.deploys {
		switch d.State {
		case DeployOK:
			good++
		case DeployFailed:
			failed++
		}
	}
	var gone []string
	for i := len(e.deploys) - 1; i >= 0; i-- {
		d := e.deploys[i]
		switch {
		case d.State == DeployOK && good > s.maxDeploys && d.ID != e.rec.Serving:
			good--
		case d.State == DeployFailed && failed > s.maxDeploys:
			failed--
		default:
			continue
		}
		gone = append(gone, d.ID)
		e.deploys = append(e.deploys[:i], e.deploys[i+1:]...)
	}
	s.mu.Unlock()

	for _, id := range gone {
		err := errors.Join(
			s.store.DeletePrefix(ctx, deployMeta(e.name, id)),
			s.store.DeletePrefix(ctx, deployFiles(e.name, id)),
		)
		if s.dir != "" {
			os.RemoveAll(filepath.Join(s.rootfsDir(e.name), id))
			os.RemoveAll(filepath.Join(s.functionsDir(e.name), id))
		}
		if err != nil {
			s.log.Error("prune deploy", "pail", e.name, "deploy", id, "err", err)
		}
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

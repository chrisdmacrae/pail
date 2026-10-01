package pails

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
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
	d := Deploy{ID: newDeployID(), State: DeployBuilding, Source: up.Source, Label: up.Label, CreatedAt: now}
	for e.hasDeploy(d.ID) {
		d.ID = newDeployID()
	}
	lg := newLog()
	e.deploys = append([]Deploy{d}, e.deploys...)
	e.logs[d.ID] = lg
	s.mu.Unlock()

	s.deploys.Add(1)
	go func() {
		defer s.deploys.Done()
		s.run(e, d, up, format, lg)
	}()
	return d, nil
}

func (e *entry) hasDeploy(id string) bool {
	for _, d := range e.deploys {
		if d.ID == id {
			return true
		}
	}
	return false
}

// run takes one deploy from archive to live. Nothing a request can see
// changes until cutover; any failure before it leaves the previous deploy
// serving.
func (s *Service) run(e *entry, d Deploy, up Upload, format archiveFormat, lg *Log) {
	e.work.Lock()
	defer e.work.Unlock()
	defer os.Remove(up.Path)
	ctx := context.Background()

	lg.add("step", "→ %s (%s)", d.Label, d.ID)
	err := s.build(ctx, e, &d, up, format, lg)

	now := time.Now().UTC()
	d.FinishedAt = &now
	if err == nil {
		d.State = DeployOK
		lg.add("ok", "✓ live at %s", up.URL)
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

func (s *Service) isRemoved(e *entry) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return e.removed
}

// build unpacks, checks and cuts over. It fills in d's file count and size.
func (s *Service) build(ctx context.Context, e *entry, d *Deploy, up Upload, format archiveFormat, lg *Log) error {
	if s.isRemoved(e) {
		return errRemoved
	}
	// The pail and the deploy exist in storage from here, so a restart
	// mid-deploy finds them and settles the deploy as failed.
	s.mu.RLock()
	rec := e.rec
	s.mu.RUnlock()
	if rec.Serving == "" {
		if err := s.writeJSON(ctx, stateKey(e.name), rec); err != nil {
			return err
		}
	}
	if err := s.writeJSON(ctx, deployKey(e.name, d.ID), d); err != nil {
		return err
	}

	man, err := s.unpack(ctx, e.name, d, up.Path, format, lg)
	if err != nil {
		return err
	}
	if err := s.writeJSON(ctx, manifestKey(e.name, d.ID), man); err != nil {
		return err
	}

	// Cutover: one write moves the live pointer.
	lg.add("step", "→ swapping %s to %s", s.host(e.name), d.ID)
	if s.isRemoved(e) {
		return errRemoved
	}
	rec.Serving = d.ID
	if err := s.writeJSON(ctx, stateKey(e.name), rec); err != nil {
		return err
	}
	s.mu.Lock()
	e.rec.Serving = d.ID
	e.manifest = man
	s.mu.Unlock()
	return nil
}

// plan is what the first pass over an archive decides.
type plan struct {
	strip    string // folder every file sits in, dropped from names
	names    map[string]bool
	pailJSON []byte
}

// unpack copies the archive's files into the deploy's own folder and returns
// the manifest of what it serves.
func (s *Service) unpack(ctx context.Context, name string, d *Deploy, archive string, format archiveFormat, lg *Log) (*Manifest, error) {
	p, err := s.survey(archive, format)
	if err != nil {
		return nil, err
	}

	var cfg staticConfig
	found := "index.html"
	if p.pailJSON != nil {
		found = "pail.json"
		if cfg, err = parsePailJSON(p.pailJSON); err != nil {
			return nil, err
		}
	}
	static := "./" + strings.TrimSuffix(cfg.root, "/")
	switch {
	case p.pailJSON == nil && !p.names["index.html"]:
		return nil, userErrorf("No index.html at the top of the upload. Point Pail at the folder that has it.")
	case cfg.fallback != "" && !p.names[cfg.root+cfg.fallback]:
		return nil, userErrorf("pail.json falls back to %s, but %s has no such file.", cfg.fallback, static)
	}

	lg.add("", "unpacking %d %s · found %s", len(p.names), plural(len(p.names), "file"), found)

	man := &Manifest{Root: cfg.root, Fallback: cfg.fallback, Files: map[string]File{}}
	prefix := deployFiles(name, d.ID)
	err = walkArchive(archive, format, func(raw string, size int64, r io.Reader) error {
		file, skip, err := cleanName(raw)
		if err != nil || skip {
			return err
		}
		file = strings.TrimPrefix(file, p.strip)
		ctype := contentType(file)
		hash := sha256.New()
		if err := s.store.Put(ctx, prefix+file, io.TeeReader(r, hash), size, ctype); err != nil {
			return err
		}
		d.Files++
		d.Bytes += size
		if served, ok := strings.CutPrefix(file, cfg.root); ok && file != "pail.json" {
			man.Files[served] = File{Size: size, ETag: hex.EncodeToString(hash.Sum(nil)[:12]), Type: ctype}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(man.Files) == 0 {
		return nil, userErrorf("pail.json points static at %s, but the upload has no files there.", static)
	}
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
		if file == "pail.json" || strings.Count(file, "/") == 1 && strings.HasSuffix(file, "/pail.json") {
			b, err := io.ReadAll(io.LimitReader(r, 1<<20))
			if err != nil {
				return userErrorf("pail.json can't be read: %v.", err)
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
	return p, nil
}

// prune deletes deploys past the limit, oldest first, never the one being
// served.
func (s *Service) prune(ctx context.Context, e *entry) {
	s.mu.Lock()
	var gone []string
	for i := len(e.deploys) - 1; i >= 0 && len(e.deploys) > s.maxDeploys; i-- {
		d := e.deploys[i]
		if d.ID == e.rec.Serving || d.State == DeployBuilding {
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

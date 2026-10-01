package pails

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/microvm"
	"github.com/chrisdmacrae/pail/internal/storage"
)

type Options struct {
	Store      storage.Store
	BaseDomain string
	MaxDeploys int
	// MaxUnpackedSize caps what one archive may unpack to, in bytes.
	MaxUnpackedSize int64
	// Builder runs builds and containers, in microVMs or in an engine's
	// containers. Nil means this Pail has neither.
	Builder microvm.Machines
	// Dir is local disk for containers' root filesystems and data volumes.
	Dir string
	// MaxContainerMemory and MaxFunctionMemory are the most memory one
	// container, or one copy of a function, may ask for, in bytes. Zero
	// means no limit.
	MaxContainerMemory int64
	MaxFunctionMemory  int64
	Logger             *slog.Logger
}

// Service is the one writer of a Pail installation's storage. It keeps every
// pail's record in memory, so a request reads the live pointer without a
// round trip.
type Service struct {
	store           storage.Store
	baseDomain      string
	maxDeploys      int
	maxUnpackedSize int64
	builder         microvm.Machines
	dir             string
	maxContainerMem int64
	maxFunctionMem  int64
	log             *slog.Logger

	deploys sync.WaitGroup

	mu       sync.RWMutex
	pails    map[string]*entry
	removing map[string]bool
	// hosts maps each custom hostname to the pail that answers at it.
	hosts map[string]string
}

// entry is one pail in memory. Everything but name and work is guarded by
// Service.mu.
type entry struct {
	name string
	// work lets one deploy (or the removal) run at a time.
	work sync.Mutex
	// recMu lets one change to the stored record happen at a time.
	recMu sync.Mutex

	rec      record
	deploys  []Deploy        // newest first
	manifest *Manifest       // of rec.Serving, loaded on first request
	logs     map[string]*Log // deploys still building
	removed  bool
	// running is the containers of the deploy being served, or nil.
	running *runset
	// output is what the pail's containers print.
	output *Log
}

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		store:           o.Store,
		baseDomain:      o.BaseDomain,
		maxDeploys:      o.MaxDeploys,
		maxUnpackedSize: o.MaxUnpackedSize,
		builder:         o.Builder,
		dir:             o.Dir,
		maxContainerMem: o.MaxContainerMemory,
		maxFunctionMem:  o.MaxFunctionMemory,
		log:             o.Logger,
		pails:           map[string]*entry{},
		removing:        map[string]bool{},
		hosts:           map[string]string{},
	}
}

// Load reads every pail from storage. Call it once, before serving.
func (s *Service) Load(ctx context.Context) error {
	keys, err := s.store.List(ctx, metaRoot)
	if err != nil {
		return err
	}
	for _, key := range keys {
		name, rest, _ := strings.Cut(strings.TrimPrefix(key, metaRoot), "/")
		switch {
		case rest == "state.json":
			e := s.entry(name)
			if err := s.readJSON(ctx, key, &e.rec); err != nil {
				return err
			}
		case strings.HasPrefix(rest, "deploys/") && strings.Count(rest, ".") == 1 && strings.HasSuffix(rest, ".json"):
			var d Deploy
			if err := s.readJSON(ctx, key, &d); err != nil {
				return err
			}
			e := s.entry(name)
			e.deploys = append(e.deploys, d)
		}
	}

	for name, e := range s.pails {
		if e.rec.Name == "" {
			// Deploy records with no pail: a removal that didn't finish.
			delete(s.pails, name)
			s.removeObjects(ctx, name)
			continue
		}
		for _, host := range e.rec.Hosts {
			s.hosts[host] = name
		}
		sort.SliceStable(e.deploys, func(i, j int) bool { return e.deploys[i].CreatedAt.After(e.deploys[j].CreatedAt) })
		for i := range e.deploys {
			if d := &e.deploys[i]; d.State == DeployBuilding {
				s.settleInterrupted(ctx, e, d)
			}
		}
	}
	return nil
}

// settleInterrupted closes a deploy that was building when Pail last stopped.
func (s *Service) settleInterrupted(ctx context.Context, e *entry, d *Deploy) {
	now := time.Now().UTC()
	d.FinishedAt = &now
	if e.rec.Serving == d.ID {
		// The pointer had already moved; only the record was behind.
		d.State = DeployOK
	} else {
		d.State = DeployFailed
		d.Error = "Pail restarted before this deploy finished."
		if err := s.store.DeletePrefix(ctx, deployFiles(e.name, d.ID)); err != nil {
			s.log.Error("clean up interrupted deploy", "pail", e.name, "deploy", d.ID, "err", err)
		}
		s.dropRootfs(ctx, e.name, d.ID)
	}
	if err := s.writeJSON(ctx, deployKey(e.name, d.ID), d); err != nil {
		s.log.Error("save deploy", "pail", e.name, "deploy", d.ID, "err", err)
	}
}

// entry returns the pail's entry, creating it. The caller holds mu, or is Load.
func (s *Service) entry(name string) *entry {
	e := s.pails[name]
	if e == nil {
		e = &entry{name: name, logs: map[string]*Log{}, output: newOutput()}
		s.pails[name] = e
	}
	return e
}

func (s *Service) host(name string) string { return name + "." + s.baseDomain }

func (s *Service) view(e *entry) Pail {
	p := Pail{
		Name:      e.name,
		Host:      s.host(e.name),
		Hosts:     append([]string{}, e.rec.Hosts...),
		Status:    StatusLive,
		Serving:   e.rec.Serving,
		CreatedAt: e.rec.CreatedAt,
		UpdatedAt: e.rec.CreatedAt,
	}
	if len(e.deploys) > 0 {
		latest := e.deploys[0]
		p.Deploy = &latest
		p.Source = latest.Source
		p.UpdatedAt = latest.CreatedAt
		if latest.FinishedAt != nil {
			p.UpdatedAt = *latest.FinishedAt
		}
		// A failure stands until the pointer next moves: choosing an older
		// deploy to serve after it makes the pail live again.
		if latest.State == DeployFailed && p.UpdatedAt.After(e.rec.ServedAt) {
			p.Status = StatusFailed
		}
	}
	if e.rec.ServedAt.After(p.UpdatedAt) {
		p.UpdatedAt = e.rec.ServedAt
	}
	if g := e.rec.Git; g != nil {
		p.Source, p.Repo, p.Revision, p.Dir = g.Host, g.Repo, g.Branch, g.Dir
	}
	if e.rec.Off {
		p.Status = StatusOff
	}
	if e.manifest != nil {
		p.Routes = e.manifest.Routes
	}
	if e.running != nil {
		for name, u := range e.running.units {
			p.Containers = append(p.Containers, ContainerStatus{Name: name, Port: u.c.Port, State: u.state()})
		}
		sort.Slice(p.Containers, func(i, j int) bool { return p.Containers[i].Name < p.Containers[j].Name })
		for name, pool := range e.running.pools {
			p.Functions = append(p.Functions, FunctionStatus{Name: name, Lang: pool.fn.Lang, Copies: pool.copies(), Max: pool.fn.Max})
		}
		sort.Slice(p.Functions, func(i, j int) bool { return p.Functions[i].Name < p.Functions[j].Name })
	}
	for _, d := range e.deploys {
		if d.State == DeployBuilding {
			p.Status = StatusBuilding
		}
	}
	return p
}

// updateRecord changes a pail's stored record and, once that is written, the
// copy requests read. man, when given, becomes the served manifest in the
// same step as the pointer that names its deploy.
func (s *Service) updateRecord(ctx context.Context, e *entry, man *Manifest, change func(*record)) error {
	return s.change(ctx, e, change, func() {
		if man != nil {
			e.manifest = man
		}
	})
}

// change is updateRecord for a caller with more to switch over than the
// manifest: apply runs once the record is written, in the same step in which
// requests start to see it.
func (s *Service) change(ctx context.Context, e *entry, change func(*record), apply func()) error {
	e.recMu.Lock()
	defer e.recMu.Unlock()
	s.mu.RLock()
	rec, removed := e.rec, e.removed
	s.mu.RUnlock()
	if removed {
		return errRemoved
	}
	change(&rec)
	if err := s.writeJSON(ctx, stateKey(e.name), rec); err != nil {
		return err
	}
	s.mu.Lock()
	e.rec = rec
	apply()
	s.mu.Unlock()
	return nil
}

// manifestOf reads what a deploy serves.
func (s *Service) manifestOf(ctx context.Context, e *entry, id string) (*Manifest, error) {
	s.mu.RLock()
	man := e.manifest
	if e.rec.Serving != id {
		man = nil
	}
	s.mu.RUnlock()
	if man != nil {
		return man, nil
	}
	man = &Manifest{}
	return man, s.readJSON(ctx, manifestKey(e.name, id), man)
}

// box is what this Pail runs builds and server code in, for messages: a
// microVM, unless the builder says otherwise.
func (s *Service) box() string {
	if named, ok := s.builder.(interface{ Sandbox() string }); ok {
		return named.Sandbox()
	}
	return "microVM"
}

// CanBuild says whether this Pail can build a project before serving it and,
// if not, why.
func (s *Service) CanBuild() (bool, string) {
	if s.builder == nil {
		return false, "it has no way to run microVMs"
	}
	return s.builder.Available()
}

// SetGit records the repo a pail deploys from, or with nil, that it no
// longer deploys from one.
func (s *Service) SetGit(ctx context.Context, name string, git *GitSource) error {
	s.mu.RLock()
	e := s.pails[name]
	s.mu.RUnlock()
	if e == nil {
		return ErrNoPail
	}
	err := s.updateRecord(ctx, e, nil, func(r *record) { r.Git = git })
	if errors.Is(err, errRemoved) {
		return ErrNoPail
	}
	return err
}

// Git returns the repo a pail deploys from, hook secret included, or nil.
func (s *Service) Git(name string) (*GitSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.pails[name]
	if e == nil {
		return nil, ErrNoPail
	}
	if e.rec.Git == nil {
		return nil, nil
	}
	git := *e.rec.Git
	return &git, nil
}

// SetOff stops a pail or starts it again. A stopped pail keeps its deploys
// and its pointer; its host just answers nothing until it is started, and
// its containers are shut down.
func (s *Service) SetOff(ctx context.Context, name string, off bool) (Pail, error) {
	s.mu.RLock()
	e := s.pails[name]
	building := e != nil && len(e.logs) > 0
	s.mu.RUnlock()
	if e == nil {
		return Pail{}, ErrNoPail
	}
	// A deploy in flight starts containers of its own when it finishes.
	if building {
		return Pail{}, ErrBuilding
	}
	e.work.Lock()
	err := s.updateRecord(ctx, e, nil, func(r *record) { r.Off = off })
	s.mu.RLock()
	running := e.running
	s.mu.RUnlock()
	e.work.Unlock()
	if errors.Is(err, errRemoved) {
		return Pail{}, ErrNoPail
	}
	if err != nil {
		return Pail{}, err
	}
	if off {
		running.halt()
	} else {
		go s.revive(e)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.view(e), nil
}

// List returns every pail, most recently updated first.
func (s *Service) List() []Pail {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]Pail, 0, len(s.pails))
	for _, e := range s.pails {
		list = append(list, s.view(e))
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].UpdatedAt.Equal(list[j].UpdatedAt) {
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		}
		return list[i].Name < list[j].Name
	})
	return list
}

func (s *Service) Get(name string) (Pail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.pails[name]
	if e == nil {
		return Pail{}, ErrNoPail
	}
	return s.view(e), nil
}

func (s *Service) Deploy(name, id string) (Deploy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.pails[name]
	if e == nil {
		return Deploy{}, ErrNoPail
	}
	for _, d := range e.deploys {
		if d.ID == id {
			return d, nil
		}
	}
	return Deploy{}, ErrNoDeploy
}

// Deploys returns the deploys a pail keeps, newest first, and the ID of the
// one being served.
func (s *Service) Deploys(name string) (deploys []Deploy, serving string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.pails[name]
	if e == nil {
		return nil, "", ErrNoPail
	}
	return append([]Deploy{}, e.deploys...), e.rec.Serving, nil
}

// Serve points a pail at one of its kept deploys: a rollback. It is the same
// single pointer write a deploy ends with, and nothing is rebuilt.
func (s *Service) Serve(ctx context.Context, name, id string) (Pail, error) {
	s.mu.RLock()
	e := s.pails[name]
	s.mu.RUnlock()
	if e == nil {
		return Pail{}, ErrNoPail
	}
	// A deploy in flight would move the pointer again when it finishes.
	if !e.work.TryLock() {
		return Pail{}, ErrBuilding
	}
	defer e.work.Unlock()

	s.mu.RLock()
	rec, removed := e.rec, e.removed
	var target *Deploy
	for i := range e.deploys {
		if e.deploys[i].ID == id {
			target = &e.deploys[i]
		}
	}
	s.mu.RUnlock()
	switch {
	case removed:
		return Pail{}, ErrNoPail
	case target == nil:
		return Pail{}, ErrNoDeploy
	case target.State != DeployOK:
		return Pail{}, ErrNotServable
	}

	if rec.Serving != id {
		man := &Manifest{}
		if err := s.readJSON(ctx, manifestKey(name, id), man); err != nil {
			return Pail{}, err
		}
		// A deploy with containers has them booted from the root filesystems
		// it was built with, and answering, before the pointer moves. That
		// outlasts a caller who hangs up.
		var lg *Log
		if len(man.Containers)+len(man.Functions) > 0 {
			lg = e.output
			lg.add("step", "→ serving %s again", id)
		}
		if err := s.goLive(context.WithoutCancel(ctx), e, id, man, lg); err != nil {
			var ue userError
			if errors.As(err, &ue) {
				return Pail{}, ErrCantStart{Reason: ue.msg}
			}
			return Pail{}, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.view(e), nil
}

// Log returns a deploy's log: the one being written if it is still building,
// else the stored one.
func (s *Service) Log(ctx context.Context, name, id string) (*Log, error) {
	if _, err := s.Deploy(name, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	var lg *Log
	if e := s.pails[name]; e != nil {
		lg = e.logs[id]
	}
	s.mu.RUnlock()
	if lg != nil {
		return lg, nil
	}
	b, err := s.store.Read(ctx, logKey(name, id))
	if errors.Is(err, storage.ErrNotFound) {
		return decodeLog(nil), nil
	}
	if err != nil {
		return nil, err
	}
	return decodeLog(b), nil
}

// Live is the deploy a pail is serving, read once per request.
type Live struct {
	Pail     string
	Deploy   string
	Manifest *Manifest
	// backends is where each of the deploy's containers answers, "" for
	// one that isn't up.
	backends map[string]string
	// run is what the deploy runs, for its functions.
	run *runset
}

// Backend is the address a container answers at. up is false while the
// container is starting, or stopped and on its way back.
func (l Live) Backend(container string) (addr string, up bool) {
	addr = l.backends[container]
	return addr, addr != ""
}

// Live reads the pail's live pointer. Everything a request then serves comes
// from that one deploy, even if the pointer moves meanwhile.
func (s *Service) Live(ctx context.Context, name string) (Live, error) {
	s.mu.RLock()
	e := s.pails[name]
	if e == nil {
		s.mu.RUnlock()
		return Live{}, ErrNoPail
	}
	live := Live{Pail: name, Deploy: e.rec.Serving, Manifest: e.manifest}
	off := e.rec.Off
	if rs := e.running; rs != nil && rs.deploy == live.Deploy {
		live.run = rs
		live.backends = make(map[string]string, len(rs.units))
		for c, u := range rs.units {
			live.backends[c] = u.addr()
		}
	}
	s.mu.RUnlock()

	if off {
		return live, ErrOff
	}

	if live.Deploy == "" {
		return live, ErrNothingLive
	}
	if live.Manifest == nil {
		live.Manifest = &Manifest{}
		if err := s.readJSON(ctx, manifestKey(name, live.Deploy), live.Manifest); err != nil {
			return live, err
		}
		s.mu.Lock()
		if e.rec.Serving == live.Deploy {
			e.manifest = live.Manifest
		}
		s.mu.Unlock()
	}
	return live, nil
}

// Open opens one file of a live deploy, by its path in the manifest.
func (s *Service) Open(ctx context.Context, live Live, file string) (io.ReadSeekCloser, error) {
	return s.store.Open(ctx, deployFiles(live.Pail, live.Deploy)+live.Manifest.Root+file)
}

// Remove deletes a pail and every deploy it has. Its host stops answering at
// once; the objects go after any deploy in flight has stopped.
func (s *Service) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	e := s.pails[name]
	if e == nil {
		s.mu.Unlock()
		return ErrNoPail
	}
	delete(s.pails, name)
	for _, host := range e.rec.Hosts {
		delete(s.hosts, host)
	}
	e.removed = true
	s.removing[name] = true
	s.mu.Unlock()

	e.work.Lock()
	defer e.work.Unlock()
	e.recMu.Lock()
	defer e.recMu.Unlock()
	// Its containers stop, and their root filesystems and data go with it.
	e.running.halt()
	err := s.removeObjects(ctx, name)
	if s.dir != "" {
		err = errors.Join(err, os.RemoveAll(s.rootfsDir(name)), os.RemoveAll(s.volumeDir(name)), os.RemoveAll(s.functionsDir(name)))
	}

	s.mu.Lock()
	delete(s.removing, name)
	s.mu.Unlock()
	return err
}

func (s *Service) removeObjects(ctx context.Context, name string) error {
	// The record goes first: without it, Load treats the rest as leftovers.
	return errors.Join(
		s.store.DeletePrefix(ctx, stateKey(name)),
		s.store.DeletePrefix(ctx, metaPrefix(name)),
		s.store.DeletePrefix(ctx, pailFiles(name)),
	)
}

// Wait blocks until no deploy is running.
func (s *Service) Wait() { s.deploys.Wait() }

func (s *Service) readJSON(ctx context.Context, key string, v any) error {
	b, err := s.store.Read(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Service) writeJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.store.Put(ctx, key, strings.NewReader(string(b)), int64(len(b)), "application/json")
}

func newDeployID() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])[:7]
}

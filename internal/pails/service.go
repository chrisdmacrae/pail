package pails

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/storage"
)

type Options struct {
	Store      storage.Store
	BaseDomain string
	MaxDeploys int
	// MaxUnpackedSize caps what one archive may unpack to, in bytes.
	MaxUnpackedSize int64
	Logger          *slog.Logger
}

// Service is the one writer of a Pail installation's storage. It keeps every
// pail's record in memory, so a request reads the live pointer without a
// round trip.
type Service struct {
	store           storage.Store
	baseDomain      string
	maxDeploys      int
	maxUnpackedSize int64
	log             *slog.Logger

	deploys sync.WaitGroup

	mu       sync.RWMutex
	pails    map[string]*entry
	removing map[string]bool
}

// entry is one pail in memory. Everything but name and work is guarded by
// Service.mu.
type entry struct {
	name string
	// work lets one deploy (or the removal) run at a time.
	work sync.Mutex

	rec      record
	deploys  []Deploy        // newest first
	manifest *Manifest       // of rec.Serving, loaded on first request
	logs     map[string]*Log // deploys still building
	removed  bool
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
		log:             o.Logger,
		pails:           map[string]*entry{},
		removing:        map[string]bool{},
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
	}
	if err := s.writeJSON(ctx, deployKey(e.name, d.ID), d); err != nil {
		s.log.Error("save deploy", "pail", e.name, "deploy", d.ID, "err", err)
	}
}

// entry returns the pail's entry, creating it. The caller holds mu, or is Load.
func (s *Service) entry(name string) *entry {
	e := s.pails[name]
	if e == nil {
		e = &entry{name: name, logs: map[string]*Log{}}
		s.pails[name] = e
	}
	return e
}

func (s *Service) host(name string) string { return name + "." + s.baseDomain }

func (s *Service) view(e *entry) Pail {
	p := Pail{
		Name:      e.name,
		Host:      s.host(e.name),
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
		if latest.State == DeployFailed {
			p.Status = StatusFailed
		}
	}
	for _, d := range e.deploys {
		if d.State == DeployBuilding {
			p.Status = StatusBuilding
		}
	}
	return p
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
	s.mu.RUnlock()

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
	e.removed = true
	s.removing[name] = true
	s.mu.Unlock()

	e.work.Lock()
	defer e.work.Unlock()
	err := s.removeObjects(ctx, name)

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

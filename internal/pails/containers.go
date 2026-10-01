package pails

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

// How containers are run. These are variables so tests needn't wait.
var (
	// PortWait is how long a container has to open its port.
	PortWait = 2 * time.Minute
	// RestartDelay is the first pause before a stopped container is
	// started again; it doubles up to restartDelayMax.
	RestartDelay = time.Second
)

const restartDelayMax = 30 * time.Second

// volumeMB is how big a data volume is. It only takes the space it uses.
const volumeMB = 10 << 10

// Where a pail's containers keep things on local disk.
//
//	<dir>/containers/<pail>/<deploy>/<c>.ext4   a root filesystem, as built
//	<dir>/volumes/<pail>/<c>.ext4               a data volume
//	<dir>/run/<pail>.<c>.<deploy>/              a running machine's own files
func (s *Service) rootfsDir(name string) string { return filepath.Join(s.dir, "containers", name) }
func (s *Service) rootfsFile(name, id, c string) string {
	return filepath.Join(s.rootfsDir(name), id, c+".ext4")
}
func (s *Service) volumeDir(name string) string { return filepath.Join(s.dir, "volumes", name) }

// unit is one container of one deploy, and the machine it runs in. Once
// started it is kept running: if what's inside stops, it is started again.
type unit struct {
	s    *Service
	e    *entry
	name string
	c    Container
	spec microvm.MachineSpec

	mu sync.Mutex
	m  microvm.Machine
	// up says the port has answered since the machine started.
	up bool
	// halted says the unit was stopped on purpose and must stay stopped.
	halted bool
	// gen counts starts and stops, so a supervisor from an earlier one
	// knows to bow out.
	gen int
	// also receives the unit's output while a deploy is watching it.
	also *Log
}

// runset is what one deploy runs: its containers, and its functions' copies.
type runset struct {
	deploy string
	units  map[string]*unit
	pools  map[string]*fnPool
}

func (rs *runset) each(fn func(*unit)) {
	if rs == nil {
		return
	}
	var wg sync.WaitGroup
	for _, u := range rs.units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(u)
		}()
	}
	wg.Wait()
}

// halt stops every machine and keeps them stopped.
func (rs *runset) halt() {
	if rs == nil {
		return
	}
	for _, p := range rs.pools {
		p.close()
	}
	rs.each((*unit).halt)
}

// resume lets a halted runset run again: containers start, and functions
// wake on their next request.
func (rs *runset) resume() {
	for _, p := range rs.pools {
		p.open()
	}
	rs.each((*unit).run)
}

func (u *unit) say(level, format string, args ...any) {
	line := Line{Time: time.Now().UTC(), Text: fmt.Sprintf(format, args...), Level: level, Source: u.name}
	u.e.output.append(line)
	u.mu.Lock()
	also := u.also
	u.mu.Unlock()
	if also != nil {
		also.add(level, "%s: %s", u.name, line.Text)
	}
}

// watchedBy sends the unit's output to a deploy's log too, or with nil,
// stops doing so.
func (u *unit) watchedBy(lg *Log) {
	if lg == u.e.output {
		lg = nil // it hears everything already
	}
	u.mu.Lock()
	u.also = lg
	u.mu.Unlock()
}

// start boots the machine and waits for its port. From then the unit is
// kept running until it is halted.
func (u *unit) start(ctx context.Context) error {
	u.mu.Lock()
	u.halted = false
	u.gen++
	gen := u.gen
	u.mu.Unlock()

	m, err := u.boot(ctx, gen)
	if err != nil {
		u.halt()
		return err
	}
	go u.supervise(gen, m)
	return nil
}

// run keeps the unit running without waiting for it: for a pail whose
// containers should be up, with nobody watching to be told how it went.
func (u *unit) run() {
	u.mu.Lock()
	if u.m != nil && !u.halted {
		u.mu.Unlock()
		return
	}
	u.halted = false
	u.gen++
	gen := u.gen
	u.mu.Unlock()
	go u.supervise(gen, nil)
}

// boot starts a machine and waits for its port to answer.
func (u *unit) boot(ctx context.Context, gen int) (microvm.Machine, error) {
	m, err := u.s.builder.Start(ctx, u.spec)
	if err != nil {
		return nil, fmt.Errorf("%s's %s didn't start: %w", u.name, u.s.box(), err)
	}
	u.mu.Lock()
	if u.gen != gen {
		u.mu.Unlock()
		m.Stop()
		return nil, errors.New(u.name + " was stopped while it was starting")
	}
	u.m, u.up = m, false
	u.mu.Unlock()

	if err := u.waitPort(ctx, m); err != nil {
		u.mu.Lock()
		if u.m == m {
			u.m = nil
		}
		u.mu.Unlock()
		m.Stop()
		return nil, err
	}
	u.mu.Lock()
	u.up = u.m == m
	u.mu.Unlock()
	return m, nil
}

// waitPort waits until the container accepts connections on its port.
func (u *unit) waitPort(ctx context.Context, m microvm.Machine) error {
	deadline := time.NewTimer(PortWait)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp", m.Addr(u.c.Port), time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-m.Done():
			return userErrorf("%s stopped before it opened port %d. Its output above says why.", u.name, u.c.Port)
		case <-deadline.C:
			return userErrorf("%s didn't open port %d within %s. It has to listen on 0.0.0.0, not 127.0.0.1, and on the port pail.json names.", u.name, u.c.Port, PortWait)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// supervise restarts the unit's machine whenever it stops, until the unit
// is halted or started afresh. m is the machine already running, or nil to
// start one.
func (u *unit) supervise(gen int, m microvm.Machine) {
	current := func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.gen == gen && !u.halted
	}
	delay := RestartDelay
	for {
		if m != nil {
			started := time.Now()
			<-m.Done()
			if !current() {
				return
			}
			u.mu.Lock()
			u.m, u.up = nil, false
			u.mu.Unlock()
			if time.Since(started) > time.Minute {
				delay = RestartDelay
			}
			u.say("error", "%s stopped. Starting it again.", u.name)
			time.Sleep(delay)
			delay = min(delay*2, restartDelayMax)
		}
		if !current() {
			return
		}
		var err error
		if m, err = u.boot(context.Background(), gen); err != nil {
			if !current() {
				return
			}
			u.say("error", "%v", err)
			time.Sleep(delay)
			delay = min(delay*2, restartDelayMax)
			continue
		}
		u.say("ok", "%s is answering on port %d.", u.name, u.c.Port)
	}
}

// halt stops the machine and keeps it stopped.
func (u *unit) halt() {
	u.mu.Lock()
	u.halted = true
	u.gen++
	m := u.m
	u.m, u.up = nil, false
	u.mu.Unlock()
	if m != nil {
		m.Stop()
	}
}

// addr is where the container answers, or "" while it isn't up.
func (u *unit) addr() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil || !u.up {
		return ""
	}
	return u.m.Addr(u.c.Port)
}

func (u *unit) state() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.halted:
		return "stopped"
	case u.m != nil && u.up:
		return "running"
	}
	return "starting"
}

// canRun says whether this Pail can run containers and, if not, why.
func (s *Service) canRun() error {
	if ok, why := s.CanBuild(); !ok {
		return userErrorf("This deploy runs server code, and this Pail can't run it: %s.", why)
	}
	if s.dir == "" {
		return userErrorf("This deploy runs server code, and this Pail has no local disk set for it.")
	}
	return nil
}

// prepare makes the units for a deploy's containers, fetching any root
// filesystem this machine doesn't have. Nothing is started.
func (s *Service) prepare(ctx context.Context, e *entry, id string, man *Manifest) (*runset, error) {
	if err := s.canRun(); err != nil {
		return nil, err
	}
	// pail.json's ${NAME} is filled in here, at each start, from the pail's
	// variables as they are now. The manifest keeps the reference.
	vars, err := s.variables(ctx, e.name)
	if err != nil {
		return nil, err
	}
	rs := &runset{deploy: id, units: map[string]*unit{}, pools: map[string]*fnPool{}}
	for name, fn := range man.Functions {
		if fn.Env, err = expandEnv(e.name, name, fn.Env, vars); err != nil {
			return nil, err
		}
		image, err := s.functionImage(ctx, e.name, id, name, fn)
		if err != nil {
			return nil, err
		}
		rs.pools[name] = &fnPool{s: s, e: e, deploy: id, name: name, fn: fn, image: image, freed: make(chan struct{})}
	}
	// A deploy's containers can reach each other, each by its name. The
	// group is the deploy's own, so one deploy's never find another's.
	var group string
	var peers []string
	if len(man.Containers) > 1 {
		group = e.name + "." + id
		for name := range man.Containers {
			peers = append(peers, name)
		}
		sort.Strings(peers)
	}
	for name, c := range man.Containers {
		rootfs, err := s.rootfs(ctx, e.name, id, name)
		if err != nil {
			return nil, err
		}
		if c.Env, err = expandEnv(e.name, name, c.Env, vars); err != nil {
			return nil, err
		}
		// Later values win: the image's own, then Pail's, then pail.json's.
		env := append([]string{}, c.ImageEnv...)
		env = append(env, "PORT="+strconv.Itoa(c.Port), "PAIL_NAME="+e.name, "PAIL_DEPLOY="+id)
		keys := make([]string, 0, len(c.Env))
		for k := range c.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = append(env, k+"="+c.Env[k])
		}
		u := &unit{s: s, e: e, name: name, c: c}
		u.spec = microvm.MachineSpec{
			Dir:    filepath.Join(s.dir, "run", e.name+"."+name+"."+id),
			Rootfs: rootfs,
			Argv:   c.argv(), Env: env, WorkingDir: c.WorkingDir, User: c.User,
			Hostname: name, Group: group, Peers: peers, LAN: s.allowLAN[e.name],
			VCPUs: c.CPUs, MemMB: c.MemoryMB,
			Log: func(line string) { u.say("", "%s", line) },
		}
		if c.Data != "" {
			u.spec.Data = filepath.Join(s.volumeDir(e.name), name+".ext4")
			u.spec.DataPath, u.spec.DataMB = c.Data, volumeMB
		}
		rs.units[name] = u
	}
	return rs, nil
}

// goLive puts a deploy's containers up, moves the live pointer to it, and
// stops the containers it replaces. If anything fails first, the pointer
// stays put and what was serving goes on serving. lg, when given, hears how
// it goes.
func (s *Service) goLive(ctx context.Context, e *entry, id string, man *Manifest, lg *Log) error {
	say := func(level, format string, args ...any) {
		if lg != nil {
			lg.add(level, format, args...)
		}
	}
	var next *runset
	s.mu.RLock()
	prev := e.running
	s.mu.RUnlock()
	// A volume holds one machine at a time. Where the new container keeps
	// data, the one it replaces has to let go first.
	var paused []*unit
	undo := func() {
		next.halt()
		for _, u := range paused {
			u.run()
		}
	}

	if len(man.Containers)+len(man.Functions) > 0 {
		var err error
		if next, err = s.prepare(ctx, e, id, man); err != nil {
			return err
		}
		names := make([]string, 0, len(next.units))
		for name := range next.units {
			names = append(names, name)
		}
		sort.Strings(names)
		if s.allowLAN[e.name] && len(next.units) > 0 {
			say("", "%s is one of PAIL_ALLOW_LAN's pails: its containers can reach the home network", e.name)
		}
		// They start together, and the deploy waits for them all: one may
		// need another before it answers, like an app its database.
		starting, giveUp := context.WithCancel(ctx)
		defer giveUp()
		started := make(chan error, len(names))
		for _, name := range names {
			u := next.units[name]
			u.watchedBy(lg)
			defer u.watchedBy(nil)
			if old := prev.unit(name); old != nil && old.spec.Data != "" && u.spec.Data != "" {
				say("", "stopping the running %s, so the new one can take its data", name)
				old.halt()
				paused = append(paused, old)
			}
			say("step", "→ starting %s in a %s (%s)", name, s.box(), u.c.size())
			go func() {
				err := u.start(starting)
				if err == nil {
					say("", "%s is answering on port %d", name, u.c.Port)
				}
				started <- err
			}()
		}
		var failed error
		for range names {
			if err := <-started; err != nil && failed == nil {
				// The first to fail is the one to hear about; the rest
				// have nothing left to wait for.
				failed = err
				giveUp()
			}
		}
		if failed != nil {
			undo()
			return failed
		}
	}

	say("step", "→ swapping %s to %s", s.host(e.name), id)
	var off bool
	err := s.change(ctx, e, func(r *record) { r.Serving, r.ServedAt = id, time.Now().UTC() }, func() {
		e.manifest, e.running, off = man, next, e.rec.Off
	})
	if err != nil {
		undo()
		return err
	}
	prev.halt()
	if off {
		// A stopped pail's deploy is checked like any other, then waits.
		next.halt()
	}
	return nil
}

func (rs *runset) unit(name string) *unit {
	if rs == nil {
		return nil
	}
	return rs.units[name]
}

// revive puts the containers of the deploy a pail is serving back up: after
// Pail restarts, or when a stopped pail is started. It doesn't wait for
// them; each is retried until it answers.
func (s *Service) revive(e *entry) {
	e.work.Lock()
	defer e.work.Unlock()
	ctx := context.Background()

	s.mu.RLock()
	rec, removed, running := e.rec, e.removed, e.running
	s.mu.RUnlock()
	if removed || rec.Off || rec.Serving == "" {
		return
	}
	if running != nil && running.deploy == rec.Serving {
		running.resume()
		return
	}
	man, err := s.manifestOf(ctx, e, rec.Serving)
	if err != nil {
		s.log.Error("read manifest", "pail", e.name, "deploy", rec.Serving, "err", err)
		return
	}
	if len(man.Containers)+len(man.Functions) == 0 {
		return
	}
	rs, err := s.prepare(ctx, e, rec.Serving, man)
	if err != nil {
		e.output.add("error", "%s's server code can't start: %v", e.name, err)
		s.log.Error("start server code", "pail", e.name, "deploy", rec.Serving, "err", err)
		return
	}
	s.mu.Lock()
	e.running = rs
	if e.manifest == nil && e.rec.Serving == rec.Serving {
		e.manifest = man
	}
	s.mu.Unlock()
	rs.each((*unit).run)
}

// Resume starts the containers of every pail that should have some running.
// Call it once, after Load.
func (s *Service) Resume() {
	if s.dir != "" {
		// No machine outlives Pail, so whatever is here is left over.
		os.RemoveAll(filepath.Join(s.dir, "run"))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.pails {
		if !e.rec.Off && e.rec.Serving != "" {
			go s.revive(e)
		}
	}
}

// Close stops every container. Call it as Pail shuts down.
func (s *Service) Close() {
	s.mu.RLock()
	var sets []*runset
	for _, e := range s.pails {
		if e.running != nil {
			sets = append(sets, e.running)
		}
	}
	s.mu.RUnlock()
	var wg sync.WaitGroup
	for _, rs := range sets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.halt()
		}()
	}
	wg.Wait()
}

// Output returns what a pail's containers have printed lately. It never
// finishes: follow it for more.
func (s *Service) Output(name string) (*Log, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.pails[name]
	if e == nil {
		return nil, ErrNoPail
	}
	return e.output, nil
}

// keepRootfs stores a root filesystem a build made with its deploy, and
// moves it to where this machine boots it from.
func (s *Service) keepRootfs(ctx context.Context, name, id, c, built string) error {
	packed := built + ".gz"
	defer os.Remove(packed)
	if err := gzipFile(built, packed); err != nil {
		return err
	}
	f, err := os.Open(packed)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := s.store.Put(ctx, rootfsKey(name, id, c), f, info.Size(), "application/gzip"); err != nil {
		return err
	}
	local := s.rootfsFile(name, id, c)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	return os.Rename(built, local)
}

// rootfs returns a deploy's root filesystem on local disk, fetching it from
// storage if this machine doesn't have it: after a rollback to a deploy
// built long ago, or on a machine whose disk was replaced.
func (s *Service) rootfs(ctx context.Context, name, id, c string) (string, error) {
	local := s.rootfsFile(name, id, c)
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", err
	}
	src, err := s.store.Open(ctx, rootfsKey(name, id, c))
	if err != nil {
		return "", fmt.Errorf("fetch %s's root filesystem: %w", c, err)
	}
	defer src.Close()
	gz, err := gzip.NewReader(src)
	if err != nil {
		return "", fmt.Errorf("fetch %s's root filesystem: %w", c, err)
	}
	if err := writeSparse(local+".tmp", gz); err != nil {
		os.Remove(local + ".tmp")
		return "", fmt.Errorf("fetch %s's root filesystem: %w", c, err)
	}
	return local, os.Rename(local+".tmp", local)
}

func gzipFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	// A root filesystem is mostly empty space; fast is plenty.
	gz, _ := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if _, err := io.Copy(gz, in); err != nil {
		out.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeSparse writes r to a file, leaving holes where r is zeros, so an
// empty stretch of a filesystem takes no room on disk.
func writeSparse(file string, r io.Reader) error {
	out, err := os.Create(file)
	if err != nil {
		return err
	}
	defer out.Close()
	const block = 64 << 10
	buf := make([]byte, block)
	var size int64
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if allZero(buf[:n]) {
				if _, err := out.Seek(int64(n), io.SeekCurrent); err != nil {
					return err
				}
			} else if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// A hole at the very end is only there once the file is that long.
	if err := out.Truncate(size); err != nil {
		return err
	}
	return out.Close()
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

package pails

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/microvm"
)

// maxFunctionOutput is the most one request's program may write back.
const maxFunctionOutput = 32 << 20

// A function's files on local disk, and the parts kept with its deploy.
//
//	<dir>/functions/<pail>/<deploy>/<f>/root.ext4   the root filesystem
//	<dir>/functions/<pail>/<deploy>/<f>/code.ext4   the function's own files
//	<dir>/functions/<pail>/<deploy>/<f>/mem, state  the snapshot
func (s *Service) functionsDir(name string) string { return filepath.Join(s.dir, "functions", name) }
func (s *Service) functionDir(name, id, f string) string {
	return filepath.Join(s.functionsDir(name), id, f)
}

var functionParts = map[string]string{"root": "root.ext4", "code": "code.ext4", "mem": "mem", "state": "state"}

// buildFunctions makes each of a deploy's functions ready to run: built
// where its language needs it, snapshotted, and kept with the deploy.
func (s *Service) buildFunctions(ctx context.Context, name string, d *Deploy, archive string, format archiveFormat, p plan, cfg deployConfig, plans map[string]functionPlan, man *Manifest, lg *Log) error {
	man.Functions = map[string]Function{}
	// Functions on the same root filesystem keep one copy of it between them.
	roots := map[string]string{}
	prevID, prev := s.servingFunctions(ctx, name)
	for f, fn := range prev {
		if _, ok := roots[fn.RootID]; !ok {
			roots[fn.RootID] = functionKey(name, prevID, f, "root")
		}
	}

	for _, f := range cfg.functionNames() {
		fc, plan := cfg.functions[f], plans[f]
		if plan.script != "" {
			lg.add("step", "→ building %s (%s) in a %s", f, plan.lang.name, s.box())
		} else {
			lg.add("step", "→ preparing %s (%s)", f, plan.lang.name)
		}
		dir := s.functionDir(name, d.ID, f)
		img, err := s.builder.BuildFunction(ctx, microvm.FunctionBuild{
			Fill:       extractFunction(archive, format, p.strip, fc.src, p.names[fc.src]),
			BuildImage: plan.lang.buildImage, Script: plan.script, RunImage: plan.lang.runImage,
			MemMB: fc.MemoryMB, Dir: dir, Warm: plan.lang.warm, WarmEnv: plan.env,
			Log: func(line string) { lg.add("", "%s", line) },
		})
		if err != nil {
			var ue userError
			if errors.As(err, &ue) {
				return err
			}
			return userErrorf("%s didn't build: %v.", f, err)
		}

		fn := fc.Function
		fn.Lang, fn.Argv, fn.RootID, fn.Snapshot = plan.lang.name, plan.argv, img.RootID, img.State != ""
		fn.BaseEnv = append(append([]string{}, img.Env...), plan.env...)
		for part, file := range functionParts {
			key := functionKey(name, d.ID, f, part)
			switch {
			case (part == "mem" || part == "state") && !fn.Snapshot:
				continue
			case part == "root" && roots[fn.RootID] != "":
				// Stored already, by another function or the last deploy.
				if err := s.store.Copy(ctx, roots[fn.RootID], key); err == nil {
					continue
				}
			}
			if err := s.putGz(ctx, key, filepath.Join(dir, file)); err != nil {
				return err
			}
		}
		roots[fn.RootID] = functionKey(name, d.ID, f, "root")
		if fn.Snapshot {
			lg.add("", "%s is ready · snapshotted, so a copy starts in a moment", f)
		} else {
			lg.add("", "%s is ready", f)
		}
		man.Functions[f] = fn
	}
	return nil
}

// servingFunctions is the functions of the deploy a pail is serving.
func (s *Service) servingFunctions(ctx context.Context, name string) (string, map[string]Function) {
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
	return id, man.Functions
}

// shareFunction gives one deploy the files another keeps for a function.
func (s *Service) shareFunction(ctx context.Context, name, from, to, f string, fn Function) error {
	for part, file := range functionParts {
		if (part == "mem" || part == "state") && !fn.Snapshot {
			continue
		}
		if err := s.store.Copy(ctx, functionKey(name, from, f, part), functionKey(name, to, f, part)); err != nil {
			return err
		}
		if s.dir != "" {
			dir := s.functionDir(name, to, f)
			if err := os.MkdirAll(dir, 0o755); err == nil {
				os.Link(filepath.Join(s.functionDir(name, from, f), file), filepath.Join(dir, file)) // else fetched from storage
			}
		}
	}
	return nil
}

// functionImage returns a function's files on local disk, fetching from
// storage whatever this machine doesn't have.
func (s *Service) functionImage(ctx context.Context, name, id, f string, fn Function) (microvm.FunctionImage, error) {
	dir := s.functionDir(name, id, f)
	img := microvm.FunctionImage{RootID: fn.RootID}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return img, err
	}
	for part, file := range functionParts {
		if (part == "mem" || part == "state") && !fn.Snapshot {
			continue
		}
		local := filepath.Join(dir, file)
		if _, err := os.Stat(local); err != nil {
			if err := s.fetchGz(ctx, functionKey(name, id, f, part), local); err != nil {
				return img, fmt.Errorf("fetch %s's %s: %w", f, part, err)
			}
		}
		switch part {
		case "root":
			img.Root = local
		case "code":
			img.Code = local
		case "mem":
			img.Mem = local
		case "state":
			img.State = local
		}
	}
	return img, nil
}

// putGz stores a local file, compressed.
func (s *Service) putGz(ctx context.Context, key, file string) error {
	packed := file + ".gz"
	defer os.Remove(packed)
	if err := gzipFile(file, packed); err != nil {
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
	return s.store.Put(ctx, key, f, info.Size(), "application/gzip")
}

// fetchGz brings a stored file back to local disk.
func (s *Service) fetchGz(ctx context.Context, key, file string) error {
	src, err := s.store.Open(ctx, key)
	if err != nil {
		return err
	}
	defer src.Close()
	gz, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	if err := writeSparse(file+".tmp", gz); err != nil {
		os.Remove(file + ".tmp")
		return err
	}
	return os.Rename(file+".tmp", file)
}

// extractFunction returns a function that unpacks a function's source from
// an archive into a folder: the files under src, or the one file src is.
func extractFunction(archive string, format archiveFormat, strip, src string, single bool) func(dir string) error {
	return func(dir string) error {
		return walkArchiveModes(archive, format, func(raw string, _ int64, mode fs.FileMode, r io.Reader) error {
			file, skip, err := cleanName(raw)
			if err != nil || skip {
				return err
			}
			file, ok := strings.CutPrefix(file, strip)
			if !ok {
				return nil // outside the pail's folder
			}
			var rel string
			switch {
			case single && file == src:
				rel = path.Base(src)
			case single:
				return nil
			case src == "":
				rel = file
			default:
				if rel, ok = strings.CutPrefix(file, src+"/"); !ok {
					return nil
				}
			}
			target := filepath.Join(dir, filepath.FromSlash(rel))
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

// fnPool is the copies of one function of one deploy: microVMs started when
// requests need them, kept warm for the next, and stopped when idle.
type fnPool struct {
	s      *Service
	e      *entry
	deploy string
	name   string
	fn     Function
	image  microvm.FunctionImage

	mu sync.Mutex
	// idle are the copies waiting for a request, the latest used last.
	idle []*fnCopy
	// total counts every copy: idle, busy, and on its way up.
	total  int
	closed bool
	// freed is closed, and replaced, whenever a copy or a place for one
	// comes free.
	freed chan struct{}
}

type fnCopy struct {
	c microvm.FunctionCopy
	// sleep stops the copy once it has been idle long enough.
	sleep *time.Timer
}

// fnSeq numbers function copies, to give each a short folder of its own.
var fnSeq atomic.Int64

func (p *fnPool) notify() {
	close(p.freed)
	p.freed = make(chan struct{})
}

// acquire returns a copy to run one request on: an idle one, a new one if
// there is room, or the first to come free.
func (p *fnPool) acquire(ctx context.Context) (*fnCopy, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrFunctionDown
		}
		if n := len(p.idle); n > 0 {
			c := p.idle[n-1]
			p.idle = p.idle[:n-1]
			c.sleep.Stop()
			p.mu.Unlock()
			return c, nil
		}
		if p.total < p.fn.Max {
			p.total++
			p.mu.Unlock()
			return p.start(ctx)
		}
		freed := p.freed
		p.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ErrFunctionBusy
		}
	}
}

// start brings one more copy up. Its place is already counted.
func (p *fnPool) start(ctx context.Context) (*fnCopy, error) {
	dir := filepath.Join(p.s.dir, "run", fmt.Sprintf("f%d", fnSeq.Add(1)))
	c, err := p.s.builder.StartFunction(ctx, microvm.FunctionSpec{
		Dir: dir, Image: p.image, MemMB: p.fn.MemoryMB,
		Log: func(line string) { p.say("", "%s", line) },
	})
	if err != nil {
		p.mu.Lock()
		p.total--
		p.notify()
		p.mu.Unlock()
		return nil, err
	}
	return &fnCopy{c: c}, nil
}

// release takes a copy back after a request. One that misbehaved is stopped
// rather than used again.
func (p *fnPool) release(c *fnCopy, healthy bool) {
	p.mu.Lock()
	if !healthy || p.closed {
		p.total--
		p.notify()
		p.mu.Unlock()
		go c.c.Stop()
		return
	}
	c.sleep = time.AfterFunc(time.Duration(p.fn.IdleMS)*time.Millisecond, func() { p.retire(c) })
	p.idle = append(p.idle, c)
	p.notify()
	p.mu.Unlock()
}

// retire stops a copy that has been idle long enough, unless a request took
// it first.
func (p *fnPool) retire(c *fnCopy) {
	p.mu.Lock()
	for i, other := range p.idle {
		if other == c {
			p.idle = append(p.idle[:i], p.idle[i+1:]...)
			p.total--
			p.notify()
			p.mu.Unlock()
			c.c.Stop()
			return
		}
	}
	p.mu.Unlock()
}

// close stops the pool's copies and refuses requests. Copies busy with a
// request stop when it ends.
func (p *fnPool) close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.total -= len(idle)
	p.notify()
	p.mu.Unlock()
	for _, c := range idle {
		c.sleep.Stop()
		c.c.Stop()
	}
}

// open lets a closed pool take requests again.
func (p *fnPool) open() {
	p.mu.Lock()
	p.closed = false
	p.mu.Unlock()
}

func (p *fnPool) copies() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

func (p *fnPool) say(level, format string, args ...any) {
	p.e.output.append(Line{Time: time.Now().UTC(), Text: fmt.Sprintf(format, args...), Level: level, Source: p.name})
}

// FunctionRequest is one request for a function, as CGI puts it.
type FunctionRequest struct {
	// Env are the request's own variables: REQUEST_METHOD and the rest.
	Env []string
	// Body is the request's body, BodyLen bytes of it.
	Body    io.Reader
	BodyLen int64
}

// FunctionFailure is a request the function's program didn't answer
// properly. Why is fit to show whoever asked.
type FunctionFailure struct{ Why string }

func (f FunctionFailure) Error() string { return f.Why }

// Invoke runs one request through a function of the live deploy and returns
// what its program wrote to standard output.
func (s *Service) Invoke(ctx context.Context, live Live, function string, req FunctionRequest) ([]byte, error) {
	p := live.run.pool(function)
	if p == nil {
		return nil, ErrFunctionDown
	}
	timeout := time.Duration(p.fn.TimeoutMS) * time.Millisecond

	// A request waits for a copy as long as it would be allowed to run.
	waiting, cancel := context.WithTimeout(ctx, timeout)
	c, err := p.acquire(waiting)
	cancel()
	switch {
	case errors.Is(err, ErrFunctionBusy) && ctx.Err() == nil:
		p.say("error", "%s was at its max of %d %s, and a request waited %s for one.", function, p.fn.Max, plural(p.fn.Max, "copy"), timeout)
		return nil, ErrFunctionBusy
	case err != nil:
		if ctx.Err() == nil && !errors.Is(err, ErrFunctionDown) {
			p.say("error", "%s couldn't start: %v", function, err)
		}
		return nil, err
	}

	env := append([]string{}, p.fn.BaseEnv...)
	env = append(env, "HOME=/tmp", "TMPDIR=/tmp")
	keys := make([]string, 0, len(p.fn.Env))
	for k := range p.fn.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+p.fn.Env[k])
	}
	// The request's own come last, so nothing in pail.json can pose as them.
	env = append(env, "PAIL_NAME="+live.Pail, "PAIL_DEPLOY="+live.Deploy)
	env = append(env, req.Env...)

	res, err := c.c.Call(ctx, microvm.FunctionCall{
		Argv: p.fn.Argv, Env: env, Dir: functionDir,
		Body: req.Body, BodyLen: req.BodyLen,
		Timeout: timeout, MaxOutput: maxFunctionOutput,
		Stderr: func(line string) { p.say("", "%s", line) },
	})
	if err != nil {
		// The copy itself went wrong, not the program: don't use it again.
		p.release(c, false)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p.say("error", "%s's %s failed: %v", function, s.box(), err)
		return nil, FunctionFailure{function + " couldn't run."}
	}
	// A program that overran or was cut off may have left the copy in a
	// state: start the next request on a fresh one.
	p.release(c, !res.TimedOut && !res.TooBig)

	why := ""
	switch {
	case res.TimedOut:
		why = fmt.Sprintf("%s ran longer than its timeout of %s and was stopped.", function, timeout)
	case res.OutOfMemory:
		why = fmt.Sprintf("%s ran out of memory: it has %s.", function, config.FormatSize(int64(p.fn.MemoryMB)<<20))
	case res.TooBig:
		why = fmt.Sprintf("%s wrote more than %s and was stopped.", function, config.FormatSize(maxFunctionOutput))
	case res.Signal != "":
		why = fmt.Sprintf("%s was stopped by %s.", function, res.Signal)
	case res.ExitCode != 0:
		why = fmt.Sprintf("%s exited with status %d.", function, res.ExitCode)
	}
	if why != "" {
		p.say("error", "%s", why)
		return nil, FunctionFailure{why}
	}
	return res.Output, nil
}

// LogFunction adds a line about a function to its pail's output: what the
// router found wrong with an answer.
func (s *Service) LogFunction(live Live, function, format string, args ...any) {
	if p := live.run.pool(function); p != nil {
		p.say("error", format, args...)
	}
}

func (rs *runset) pool(name string) *fnPool {
	if rs == nil {
		return nil
	}
	return rs.pools[name]
}

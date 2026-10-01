package engine

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

// A function runs in a container made from an image of its own: the image
// of its language, with the function's files and Pail's agent added. A copy
// is one such container, with the agent waiting in it for requests.
//
// What a deploy keeps of a function is enough to make that image again on
// any engine: which image it starts from, and its files. They go where a
// microVM's disks would, under the same names, because the names are how
// the deploy finds them.
const (
	functionRoot = "root.ext4" // which image the function starts from
	functionCode = "code.ext4" // the function's files, as a tar archive
)

const (
	// agentPath is where Pail's own binary sits in a function's image.
	agentPath = "/.pail-agent"
	// agentPort is where the agent listens, on Pail's network.
	agentPort = 7911
	// agentTokenEnv holds what a request to the agent has to carry: other
	// containers on the network can reach the agent too.
	agentTokenEnv = "PAIL_AGENT_TOKEN"
	// agentStart is how long a copy's agent has to answer.
	agentStart = 30 * time.Second
)

// IsAgent reports whether this process is the agent in a function's
// container: Pail's binary, run from where the function's image keeps it.
func IsAgent() bool {
	return len(os.Args) > 0 && os.Args[0] == agentPath
}

// AgentMain is the agent in a function's container. It runs the function's
// program afresh for each request Pail sends, until Pail stops the
// container.
func AgentMain() {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(agentPort))
	if err == nil {
		err = microvm.ServeAgent(l, os.Getenv(agentTokenEnv))
	}
	fmt.Fprintln(os.Stderr, "pail:", err)
	os.Exit(1)
}

// base says which image a function starts from.
type base struct {
	Image string `json:"image"`
}

// BuildFunction makes a function ready to run: its source built, where the
// language needs that, in a throwaway container; then its image made.
func (r *Runner) BuildFunction(ctx context.Context, req microvm.FunctionBuild) (microvm.FunctionImage, error) {
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, microvm.FunctionBuildTimeout)
	defer cancel()
	var fn microvm.FunctionImage

	tmp, err := r.buildDir("f-")
	if err != nil {
		return fn, err
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fn, err
	}
	if err := req.Fill(src); err != nil {
		return fn, err
	}
	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return fn, err
	}

	// What the function runs from: its source as it is, or as built. Either
	// way an archive with everything under src/.
	var code io.ReadCloser
	if req.Script != "" {
		if _, err := r.pull(ctx, req.BuildImage, log); err != nil {
			return fn, err
		}
		id, exit, err := r.run(ctx, job{Image: qualify(req.BuildImage), Script: microvm.FunctionPrelude + req.Script, Src: src, Log: log})
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			return fn, fmt.Errorf("the build ran for %s and was stopped", microvm.FunctionBuildTimeout)
		case err != nil:
			return fn, err
		}
		defer r.discard(id)
		if exit != 0 {
			return fn, fmt.Errorf("the build exited with status %d", exit)
		}
		if code, err = r.api.copyOut(ctx, id, "/work/src"); err != nil {
			return fn, err
		}
	} else {
		code = archive(func(w *tar.Writer) error { return tarDir(w, src, "src") })
	}
	fn.Code = filepath.Join(req.Dir, functionCode)
	err = writeFile(fn.Code, code)
	code.Close()
	if err != nil {
		return fn, err
	}

	// The image the function starts from is kept by its digest, so that the
	// copies of this deploy run on what it was built with, whatever the tag
	// comes to mean.
	info, err := r.pull(ctx, req.RunImage, log)
	if err != nil {
		return fn, err
	}
	from := qualify(req.RunImage)
	if len(info.RepoDigests) > 0 {
		from = qualify(info.RepoDigests[0])
	}
	raw, _ := json.Marshal(base{Image: from})
	fn.Root = filepath.Join(req.Dir, functionRoot)
	if err := os.WriteFile(fn.Root, raw, 0o644); err != nil {
		return fn, err
	}
	sum := sha256.Sum256([]byte(from))
	fn.RootID = hex.EncodeToString(sum[:8])
	fn.Env = info.Config.Env

	// Made now, so that a function whose image can't be made fails its
	// deploy rather than its first request.
	if _, err := r.functionImage(ctx, fn); err != nil {
		return fn, err
	}
	return fn, nil
}

// functionImage returns the image a function's copies run, making it from
// what the deploy keeps if the engine doesn't have it.
func (r *Runner) functionImage(ctx context.Context, fn microvm.FunctionImage) (string, error) {
	r.mu.Lock()
	ref := r.refs[fn.Code]
	once := r.making[fn.Code]
	if once == nil {
		once = &sync.Mutex{}
		r.making[fn.Code] = once
	}
	r.mu.Unlock()
	once.Lock()
	defer once.Unlock()

	self, selfID, err := r.executable()
	if err != nil {
		return "", err
	}
	var from base
	if ref == "" {
		raw, err := os.ReadFile(fn.Root)
		if err != nil {
			return "", err
		}
		if json.Unmarshal(raw, &from) != nil || from.Image == "" {
			// A Pail that runs microVMs keeps a root filesystem here instead.
			return "", fmt.Errorf("what this deploy keeps isn't for containers; it was made by a Pail that runs microVMs. Deploy it again")
		}
		code, err := hashFile(fn.Code)
		if err != nil {
			return "", err
		}
		// The image holds this binary as its agent, so one made by another
		// build of Pail is not this one.
		sum := sha256.Sum256([]byte(from.Image + "\n" + code + "\n" + selfID))
		ref = r.tag("fn", hex.EncodeToString(sum[:12]))
		r.mu.Lock()
		r.refs[fn.Code] = ref
		r.mu.Unlock()
	}
	r.use(ref)
	if _, err := r.api.image(ctx, ref); err == nil {
		return ref, nil
	}
	if from.Image == "" {
		raw, err := os.ReadFile(fn.Root)
		if err != nil || json.Unmarshal(raw, &from) != nil {
			return "", fmt.Errorf("can't read which image the function starts from")
		}
	}
	if _, err := r.pull(ctx, from.Image, func(string) {}); err != nil {
		return "", err
	}

	dockerfile := "FROM " + from.Image + "\nCOPY agent " + agentPath + "\nCOPY src /work/src\n"
	buildContext := archive(func(w *tar.Writer) error {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
			return err
		}
		if _, err := w.Write([]byte(dockerfile)); err != nil {
			return err
		}
		if err := tarFile(w, self, "agent"); err != nil {
			return err
		}
		code, err := os.Open(fn.Code)
		if err != nil {
			return err
		}
		defer code.Close()
		return tarCopy(w, code)
	})
	defer buildContext.Close()
	if err := r.api.build(ctx, ref, "Dockerfile", r.labels(), buildContext, nil); err != nil {
		return "", fmt.Errorf("can't make the function's image: %w", err)
	}
	return ref, nil
}

// tarCopy writes every entry of an archive to w.
func tarCopy(w *tar.Writer, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		head, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := w.WriteHeader(head); err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			return err
		}
	}
}

// functionCopy is one running container of a function.
type functionCopy struct {
	r     *Runner
	id    string
	addr  string
	token string
	gone  chan struct{}
	stop  sync.Once
}

// StartFunction starts one copy of a function and waits for its agent.
func (r *Runner) StartFunction(ctx context.Context, spec microvm.FunctionSpec) (microvm.FunctionCopy, error) {
	ref, err := r.functionImage(ctx, spec.Image)
	if err != nil {
		return nil, err
	}
	token := randomHex(16)
	id, err := r.api.create(ctx, r.cfg.Network+"-fn."+filepath.Base(spec.Dir), containerConfig{
		Image: ref, Entrypoint: []string{agentPath}, Env: []string{agentTokenEnv + "=" + token},
		WorkingDir: "/", User: "0:0", Hostname: "pail", Labels: r.labels(),
		HostConfig: hostConfig{Memory: int64(spec.MemMB) << 20, NetworkMode: r.cfg.Network},
	})
	if err != nil {
		return nil, err
	}
	if err := r.api.start(ctx, id); err != nil {
		r.discard(id)
		return nil, err
	}
	ip, err := r.address(ctx, id)
	if err != nil {
		r.discard(id)
		return nil, err
	}
	c := &functionCopy{r: r, id: id, addr: net.JoinHostPort(ip, strconv.Itoa(agentPort)), token: token, gone: make(chan struct{})}
	var said strings.Builder
	var saidMu sync.Mutex
	go r.api.logs(context.Background(), id, func(line string) {
		saidMu.Lock()
		if said.Len() < 4<<10 {
			said.WriteString(line + "\n")
		}
		saidMu.Unlock()
		if spec.Log != nil {
			spec.Log(line)
		}
	})
	go func() {
		r.ended(id)
		r.discard(id)
		close(c.gone)
	}()

	deadline := time.Now().Add(agentStart)
	for {
		err := c.hello(ctx)
		if err == nil {
			return c, nil
		}
		select {
		case <-c.gone:
			saidMu.Lock()
			defer saidMu.Unlock()
			return nil, fmt.Errorf("the function's container stopped as it started: %s", strings.TrimSpace(said.String()))
		case <-ctx.Done():
			c.Stop()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			c.Stop()
			return nil, fmt.Errorf("the agent inside the function's container didn't answer: %w", err)
		}
	}
}

func (c *functionCopy) dial() (net.Conn, error) {
	return net.DialTimeout("tcp", c.addr, 2*time.Second)
}

func (c *functionCopy) hello(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	return microvm.AgentHello(ctx, conn, c.token)
}

// Call runs one program in the copy and waits for it to end.
func (c *functionCopy) Call(ctx context.Context, call microvm.FunctionCall) (microvm.FunctionResult, error) {
	conn, err := c.dial()
	if err != nil {
		return microvm.FunctionResult{}, err
	}
	defer conn.Close()
	return microvm.AgentCall(ctx, conn, c.token, call)
}

func (c *functionCopy) Done() <-chan struct{} { return c.gone }

func (c *functionCopy) Stop() {
	c.stop.Do(func() { c.r.discard(c.id) })
	<-c.gone
}

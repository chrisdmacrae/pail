package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// api speaks to a container engine over its socket, in the API Docker made
// and Podman answers too. Paths carry no version, so each engine answers
// with the newest it knows.
type api struct {
	hc *http.Client
}

func newAPI(socket string) *api {
	return &api{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}

// apiError is the engine saying no.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return e.Message }

// notFound says the engine has no such thing.
func notFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do sends one request. The caller closes the answer's body.
func (a *api) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	target := "http://engine" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("the container engine didn't answer: %w", err)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		resp.Body.Close()
		var said struct {
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &said)
		if said.Message == "" {
			said.Message = strings.TrimSpace(string(raw))
		}
		if said.Message == "" {
			said.Message = resp.Status
		}
		return nil, &apiError{Status: resp.StatusCode, Message: said.Message}
	}
	return resp, nil
}

// call sends in as JSON, if there is any, and reads the answer into out, if
// one is wanted.
func (a *api) call(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(raw), "application/json"
	}
	resp, err := a.do(ctx, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// progress reads what the engine says while it pulls, builds or loads: a
// run of JSON objects. An error among them is how such a job fails, since
// by then the engine has already answered 200.
func progress(r io.Reader, line func(string)) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("the container engine stopped part-way: %w", err)
		}
		if msg.Error != "" {
			return errors.New(strings.TrimSpace(msg.Error))
		}
		if line == nil {
			continue
		}
		for _, text := range strings.Split(msg.Stream, "\n") {
			if text = cleanLine(text); text != "" {
				line(text)
			}
		}
	}
}

// containerConfig is a container to make.
type containerConfig struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	User       string            `json:"User,omitempty"`
	Hostname   string            `json:"Hostname,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	Memory      int64    `json:"Memory,omitempty"`
	NanoCPUs    int64    `json:"NanoCpus,omitempty"`
	NetworkMode string   `json:"NetworkMode,omitempty"`
	Binds       []string `json:"Binds,omitempty"`
}

// create makes a container, replacing any that has its name, and returns
// its ID. Nothing runs until it is started.
func (a *api) create(ctx context.Context, name string, cfg containerConfig) (string, error) {
	var made struct {
		ID string `json:"Id"`
	}
	query := url.Values{}
	if name != "" {
		a.remove(ctx, name)
		query.Set("name", name)
	}
	err := a.call(ctx, "POST", "/containers/create", query, cfg, &made)
	if err != nil && (cfg.HostConfig.Memory > 0 || cfg.HostConfig.NanoCPUs > 0) && !notFound(err) {
		// An engine that runs without root can't always set limits. Running
		// without them beats not running.
		first := err
		cfg.HostConfig.Memory, cfg.HostConfig.NanoCPUs = 0, 0
		if err = a.call(ctx, "POST", "/containers/create", query, cfg, &made); err != nil {
			err = first
		}
	}
	return made.ID, err
}

// connect puts a container on another of the engine's networks, where it
// answers to alias.
func (a *api) connect(ctx context.Context, network, id, alias string) error {
	return a.call(ctx, "POST", "/networks/"+network+"/connect", nil, map[string]any{
		"Container":      id,
		"EndpointConfig": map[string]any{"Aliases": []string{alias}},
	}, nil)
}

func (a *api) start(ctx context.Context, id string) error {
	return a.call(ctx, "POST", "/containers/"+id+"/start", nil, nil, nil)
}

// wait returns once the container has stopped, with its exit status.
func (a *api) wait(ctx context.Context, id string) (int, error) {
	var ended struct {
		StatusCode int `json:"StatusCode"`
	}
	err := a.call(ctx, "POST", "/containers/"+id+"/wait", url.Values{"condition": {"not-running"}}, nil, &ended)
	return ended.StatusCode, err
}

// stop asks what the container runs to finish, and kills it if it hasn't
// after seconds.
func (a *api) stop(ctx context.Context, id string, seconds int) error {
	return a.call(ctx, "POST", "/containers/"+id+"/stop", url.Values{"t": {fmt.Sprint(seconds)}}, nil, nil)
}

// remove deletes a container, running or not, and the volumes only it had.
func (a *api) remove(ctx context.Context, id string) error {
	err := a.call(ctx, "DELETE", "/containers/"+id, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if notFound(err) {
		return nil
	}
	return err
}

// state is what the engine knows of a container.
type state struct {
	State struct {
		Running   bool `json:"Running"`
		OOMKilled bool `json:"OOMKilled"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (a *api) inspect(ctx context.Context, id string) (state, error) {
	var st state
	return st, a.call(ctx, "GET", "/containers/"+id+"/json", nil, nil, &st)
}

// copyIn unpacks a tar archive into a folder inside a container.
func (a *api) copyIn(ctx context.Context, id, dir string, archive io.Reader) error {
	resp, err := a.do(ctx, "PUT", "/containers/"+id+"/archive", url.Values{"path": {dir}}, archive, "application/x-tar")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// copyOut returns a file or folder inside a container as a tar archive,
// whose entries start with the last part of its path.
func (a *api) copyOut(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, err := a.do(ctx, "GET", "/containers/"+id+"/archive", url.Values{"path": {path}}, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// has says whether a container has something at path.
func (a *api) has(ctx context.Context, id, path string) bool {
	resp, err := a.do(ctx, "HEAD", "/containers/"+id+"/archive", url.Values{"path": {path}}, nil, "")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// logs sends what a container prints to line, a line at a time, until the
// container stops.
func (a *api) logs(ctx context.Context, id string, line func(string)) error {
	resp, err := a.do(ctx, "GET", "/containers/"+id+"/logs", url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}}, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	lines := &lineWriter{line: line}
	defer lines.flush()
	return demux(resp.Body, lines)
}

// demux undoes the framing an engine puts on a container's output: eight
// bytes ahead of each piece, saying which stream it is from and how long it
// is. Both streams go to w.
func demux(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	for {
		head, err := br.Peek(8)
		if err == io.EOF {
			// The last of an output that was never framed.
			_, err := io.Copy(w, br)
			return err
		}
		if err != nil {
			return err
		}
		if head[0] > 2 || head[1] != 0 || head[2] != 0 || head[3] != 0 {
			// Not framed: an engine sends a terminal's output as it is.
			_, err := io.Copy(w, br)
			return err
		}
		br.Discard(8)
		if _, err := io.CopyN(w, br, int64(binary.BigEndian.Uint32(head[4:]))); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// lineWriter turns what is written to it into lines.
type lineWriter struct {
	line func(string)
	buf  bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		text, rest, found := bytes.Cut(w.buf.Bytes(), []byte("\n"))
		if !found {
			return len(p), nil
		}
		w.emit(string(text))
		w.buf = *bytes.NewBuffer(append([]byte(nil), rest...))
	}
}

func (w *lineWriter) flush() {
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
}

func (w *lineWriter) emit(text string) {
	if text = cleanLine(text); text != "" && w.line != nil {
		w.line(text)
	}
}

// Colours and cursor moves a tool prints for a terminal.
var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

// cleanLine makes a line of output fit for a log.
func cleanLine(line string) string {
	return strings.TrimRight(ansi.ReplaceAllString(strings.TrimRight(line, "\r\n"), ""), " ")
}

// imageInfo is what an image says about itself.
type imageInfo struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Env        []string `json:"Env"`
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		WorkingDir string   `json:"WorkingDir"`
		User       string   `json:"User"`
	} `json:"Config"`
}

func (a *api) image(ctx context.Context, ref string) (imageInfo, error) {
	var info imageInfo
	return info, a.call(ctx, "GET", "/images/"+ref+"/json", nil, nil, &info)
}

// pull fetches an image from its registry.
func (a *api) pull(ctx context.Context, ref string) error {
	resp, err := a.do(ctx, "POST", "/images/create", url.Values{"fromImage": {ref}}, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return progress(resp.Body, nil)
}

// build makes an image from a build context, a tar archive with a
// Dockerfile in it, and names it tag.
func (a *api) build(ctx context.Context, tag, dockerfile string, labels map[string]string, buildContext io.Reader, line func(string)) error {
	query := url.Values{"t": {tag}, "dockerfile": {dockerfile}, "rm": {"1"}, "forcerm": {"1"}}
	if len(labels) > 0 {
		raw, _ := json.Marshal(labels)
		query.Set("labels", string(raw))
	}
	resp, err := a.do(ctx, "POST", "/build", query, buildContext, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return progress(resp.Body, line)
}

// save returns an image as an archive that load reads back.
func (a *api) save(ctx context.Context, ref string) (io.ReadCloser, error) {
	resp, err := a.do(ctx, "GET", "/images/"+ref+"/get", nil, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// load gives the engine an image from an archive.
func (a *api) load(ctx context.Context, archive io.Reader) error {
	resp, err := a.do(ctx, "POST", "/images/load", url.Values{"quiet": {"1"}}, archive, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return progress(resp.Body, nil)
}

func (a *api) removeImage(ctx context.Context, ref string) error {
	return a.call(ctx, "DELETE", "/images/"+ref, nil, nil, nil)
}

// filter is how the engine is asked for the things with a label.
func filter(label string) url.Values {
	raw, _ := json.Marshal(map[string][]string{"label": {label}})
	return url.Values{"filters": {string(raw)}}
}

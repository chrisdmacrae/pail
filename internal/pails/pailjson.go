package pails

import (
	"encoding/json"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
	imagename "github.com/google/go-containerregistry/pkg/name"
)

// pailJSON is pail.json as it is written.
type pailJSON struct {
	Name   string `json:"name"`
	Static string `json:"static"`
	// Watch lists folders and files outside the pail's own, as paths from
	// the top of its repo, that a push redeploys the pail for.
	Watch      []string                 `json:"watch"`
	Functions  map[string]functionJSON  `json:"functions"`
	Containers map[string]containerJSON `json:"containers"`
	Routes     []struct {
		Path     string `json:"path"`
		To       string `json:"to"`
		Fallback string `json:"fallback"`
	} `json:"routes"`
}

type functionJSON struct {
	// Src is the function's source: a folder, or one file.
	Src  string `json:"src"`
	Lang string `json:"lang"`
	// Cmd is a command line, or a list of words.
	Cmd json.RawMessage `json:"cmd"`
	// Timeout and Idle are lengths of time like "10s" and "5m".
	Timeout json.RawMessage   `json:"timeout"`
	Idle    json.RawMessage   `json:"idle"`
	Memory  json.RawMessage   `json:"memory"`
	Max     *int              `json:"max"`
	Env     map[string]string `json:"env"`
}

type containerJSON struct {
	// Image names an image in a registry to run as it is; Dockerfile, one
	// to build. A container has one or the other.
	Image      string  `json:"image"`
	Dockerfile string  `json:"dockerfile"`
	Context    *string `json:"context"`
	Port       int     `json:"port"`
	CPUs       *int    `json:"cpus"`
	// Memory is a size like "512MB", or a bare number of megabytes.
	Memory json.RawMessage   `json:"memory"`
	Data   string            `json:"data"`
	Env    map[string]string `json:"env"`
	// Command is a list of words to run in place of the image's CMD.
	Command json.RawMessage `json:"command"`
}

// What a container gets when pail.json doesn't say. Memory has no default:
// every container says how much it may use.
const (
	defaultContainerCPUs = 1
	minContainerMemoryMB = 32
)

// What a function gets when pail.json doesn't say.
const (
	defaultFunctionTimeout  = 10 * time.Second
	defaultFunctionIdle     = 5 * time.Minute
	defaultFunctionMemoryMB = 128
	defaultFunctionMax      = 4
	maxFunctionCopies       = 64
	maxFunctionTimeout      = 15 * time.Minute
)

// deployConfig is what a deploy takes from pail.json.
type deployConfig struct {
	root     string // "" or "build/"
	fallback string // relative to root
	// files says the deploy serves files. A deploy with containers serves
	// files only from the folder pail.json names: its source isn't a site.
	files      bool
	containers map[string]containerConfig
	functions  map[string]functionConfig
	routes     []Route
}

// static is the folder pail.json names for the deploy's files, or "" when it
// names none.
func (c deployConfig) static() string {
	return strings.TrimSuffix(c.root, "/")
}

// functionConfig is one function as pail.json declares it.
type functionConfig struct {
	// src is the source's path inside the upload: a folder or a file.
	src  string
	lang string
	// cmd, when set, is run in place of the language's own command.
	cmd []string
	Function
}

// functionNames lists the functions in a steady order.
func (c deployConfig) functionNames() []string {
	names := make([]string, 0, len(c.functions))
	for name := range c.functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// containerConfig is one container as pail.json declares it.
type containerConfig struct {
	// dockerfile and context are paths inside the upload, for a container
	// that is built. One that runs an image has neither.
	dockerfile, context string
	Container
}

// names lists the containers in a steady order.
func (c deployConfig) names() []string {
	names := make([]string, 0, len(c.containers))
	for name := range c.containers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var (
	containerNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	envNameRE       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func parsePailJSON(b []byte) (deployConfig, error) {
	c := deployConfig{files: true}
	var pj pailJSON
	if err := json.Unmarshal(b, &pj); err != nil {
		return c, userErrorf("pail.json isn't valid JSON: %v.", err)
	}
	root, err := relPath(pj.Static)
	if err != nil {
		return c, userErrorf("pail.json: static must be a folder inside the upload, like ./dist.")
	}
	for _, w := range pj.Watch {
		if _, err := relPath(w); err != nil {
			return c, userErrorf("pail.json: watch lists folders and files by their path from the top of the repo, like packages/ui. Got %q.", w)
		}
	}
	if root != "" {
		c.root = root + "/"
	}

	if len(pj.Containers)+len(pj.Functions) > 0 {
		c.containers = map[string]containerConfig{}
		c.functions = map[string]functionConfig{}
		c.files = strings.TrimSpace(pj.Static) != ""
	}
	for name, fj := range pj.Functions {
		fc, err := parseFunction(name, fj)
		if err != nil {
			return c, err
		}
		c.functions[name] = fc
	}
	for name, cj := range pj.Containers {
		cc, err := parseContainer(name, cj)
		if err != nil {
			return c, err
		}
		c.containers[name] = cc
	}

	for _, r := range pj.Routes {
		kind, target, _ := strings.Cut(r.To, ":")
		switch {
		case r.To == "static":
			if !c.files {
				return c, userErrorf("pail.json routes %s to static, but doesn't say which folder holds the files. Set \"static\" to it, like ./public.", r.Path)
			}
			if r.Fallback != "" && c.fallback == "" {
				if c.fallback, err = relPath(r.Fallback); err != nil || c.fallback == "" {
					return c, userErrorf("pail.json: fallback must be a file inside the static folder, like index.html.")
				}
			}
		case kind == "container":
			if _, ok := c.containers[target]; !ok {
				return c, userErrorf("pail.json routes %s to container %q, and declares no container by that name.", r.Path, target)
			}
		case kind == "function":
			if _, ok := c.functions[target]; !ok {
				return c, userErrorf("pail.json routes %s to function %q, and declares no function by that name.", r.Path, target)
			}
		default:
			return c, userErrorf("pail.json routes %s to %q. A route goes to \"static\", \"function:<name>\" or \"container:<name>\".", r.Path, r.To)
		}
		if len(c.containers)+len(c.functions) == 0 {
			continue // with only files to serve, every path is theirs
		}
		if !strings.HasPrefix(r.Path, "/") {
			return c, userErrorf("pail.json: a route's path starts with a slash, like /api/*. Got %q.", r.Path)
		}
		if i := strings.Index(r.Path, "*"); i >= 0 && i != len(r.Path)-1 {
			return c, userErrorf("pail.json: a route's path may end in *, and have none elsewhere. Got %q.", r.Path)
		}
		c.routes = append(c.routes, Route{Path: r.Path, To: r.To})
	}

	// With no routes, guess: one container or function and nothing else
	// takes every path.
	if len(c.containers)+len(c.functions) > 0 && len(c.routes) == 0 {
		var targets []string
		for _, name := range c.names() {
			targets = append(targets, "container:"+name)
		}
		for _, name := range c.functionNames() {
			targets = append(targets, "function:"+name)
		}
		if len(targets) > 1 || c.files {
			return c, userErrorf("pail.json has more than one thing to answer requests, so it needs routes to say which paths go where, like {\"path\": \"/api/*\", \"to\": \"%s\"}.", targets[0])
		}
		c.routes = []Route{{Path: "/*", To: targets[0]}}
	}
	return c, nil
}

func parseFunction(name string, fj functionJSON) (functionConfig, error) {
	var fc functionConfig
	if !containerNameRE.MatchString(name) {
		return fc, userErrorf("pail.json: function names are lowercase letters, numbers and dashes, like api. Got %q.", name)
	}
	if strings.TrimSpace(fj.Src) == "" {
		return fc, userErrorf("pail.json: say where %s's source is, like \"src\": \"./fn/%s\".", name, name)
	}
	var err error
	if fc.src, err = relPath(fj.Src); err != nil {
		return fc, userErrorf("pail.json: %s's src must be a folder or a file inside the upload, like ./fn/%s.", name, name)
	}
	if fj.Lang != "" && languageNamed(fj.Lang) == nil {
		return fc, userErrorf("pail.json: %s's lang is %q, and Pail runs %s.", name, fj.Lang, languageNames())
	}
	fc.lang = fj.Lang

	if len(fj.Cmd) > 0 && string(fj.Cmd) != "null" {
		var line string
		switch {
		case json.Unmarshal(fj.Cmd, &line) == nil && strings.TrimSpace(line) != "":
			fc.cmd = []string{"sh", "-c", line}
		case json.Unmarshal(fj.Cmd, &fc.cmd) == nil && len(fc.cmd) > 0:
		default:
			return fc, userErrorf("pail.json: %s's cmd must be a command line, like \"python3 app.py\", or a list of words.", name)
		}
	}

	timeout, err := parseDuration(fj.Timeout, defaultFunctionTimeout)
	if err != nil || timeout <= 0 || timeout > maxFunctionTimeout {
		return fc, userErrorf("pail.json: %s's timeout must be a length of time up to %s, like 10s.", name, maxFunctionTimeout)
	}
	idle, err := parseDuration(fj.Idle, defaultFunctionIdle)
	if err != nil || idle <= 0 {
		return fc, userErrorf("pail.json: %s's idle must be a length of time, like 5m.", name)
	}
	fc.TimeoutMS, fc.IdleMS = timeout.Milliseconds(), idle.Milliseconds()

	fc.MemoryMB = defaultFunctionMemoryMB
	if len(fj.Memory) > 0 && string(fj.Memory) != "null" {
		if fc.MemoryMB, err = parseMemory(fj.Memory); err != nil {
			return fc, userErrorf("pail.json: %s's memory must be a size like 256MB.", name)
		}
		if fc.MemoryMB < minContainerMemoryMB {
			return fc, userErrorf("pail.json: %s asks for less memory than a microVM boots in. Ask for at least %dMB.", name, minContainerMemoryMB)
		}
	}
	fc.Max = defaultFunctionMax
	if fj.Max != nil {
		fc.Max = *fj.Max
	}
	if fc.Max < 1 || fc.Max > maxFunctionCopies {
		return fc, userErrorf("pail.json: %s's max must be between 1 and %d. Got %d.", name, maxFunctionCopies, fc.Max)
	}
	for key := range fj.Env {
		if !envNameRE.MatchString(key) {
			return fc, userErrorf("pail.json: %s has an environment variable called %q. Names are letters, numbers and underscores.", name, key)
		}
	}
	fc.Env = fj.Env
	return fc, nil
}

// parseDuration reads a length of time from pail.json: "10s", "5m", or a
// bare number of seconds.
func parseDuration(raw json.RawMessage, fallback time.Duration) (time.Duration, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return time.ParseDuration(strings.ReplaceAll(text, " ", ""))
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return 0, err
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// parseMemory reads a size from pail.json, in megabytes: "512MB", or a bare
// number of megabytes.
func parseMemory(raw json.RawMessage) (int, error) {
	var size string
	if json.Unmarshal(raw, &size) == nil {
		bytes, err := config.ParseSize(size)
		return int(bytes >> 20), err
	}
	var mb float64
	if err := json.Unmarshal(raw, &mb); err != nil {
		return 0, err
	}
	return int(mb), nil
}

func parseContainer(name string, cj containerJSON) (containerConfig, error) {
	var cc containerConfig
	if !containerNameRE.MatchString(name) {
		return cc, userErrorf("pail.json: container names are lowercase letters, numbers and dashes, like api. Got %q.", name)
	}
	var err error
	if image := strings.TrimSpace(cj.Image); image != "" {
		if cj.Dockerfile != "" || cj.Context != nil {
			return cc, userErrorf("pail.json: %s has both an image and a dockerfile. Give it one: an image to run as it is, or a Dockerfile to build.", name)
		}
		if _, err := imagename.ParseReference(image); err != nil {
			return cc, userErrorf("pail.json: %s's image must name an image in a registry, like nginx:1.27 or ghcr.io/owner/app:latest. Got %q.", name, image)
		}
		cc.Image = image
	} else {
		if cj.Dockerfile == "" {
			cj.Dockerfile = "Dockerfile"
		}
		if cc.dockerfile, err = relPath(cj.Dockerfile); err != nil || cc.dockerfile == "" {
			return cc, userErrorf("pail.json: %s's dockerfile must be a file inside the upload, like ./Dockerfile.", name)
		}
		// The context defaults to the Dockerfile's folder.
		cc.context = path.Dir(cc.dockerfile)
		if cj.Context != nil {
			if cc.context, err = relPath(*cj.Context); err != nil {
				return cc, userErrorf("pail.json: %s's context must be a folder inside the upload.", name)
			}
		}
		if cc.context == "" {
			cc.context = "."
		}
	}

	switch {
	case cj.Port == 0:
		return cc, userErrorf("pail.json: say which port %s listens on, like \"port\": 8080.", name)
	case cj.Port < 2 || cj.Port > 65535:
		return cc, userErrorf("pail.json: %s's port must be between 2 and 65535. Got %d.", name, cj.Port)
	}
	cc.Port = cj.Port

	cc.CPUs = defaultContainerCPUs
	if cj.CPUs != nil {
		cc.CPUs = *cj.CPUs
	}
	if most := min(runtime.NumCPU(), 32); cc.CPUs < 1 || cc.CPUs > most {
		return cc, userErrorf("pail.json: %s asks for %d cpus, and this Pail's server has %d. Ask for between 1 and %d.", name, cc.CPUs, runtime.NumCPU(), most)
	}

	if len(cj.Memory) == 0 || string(cj.Memory) == "null" {
		return cc, userErrorf("pail.json: say how much memory %s may use, like \"memory\": \"256MB\".", name)
	}
	{
		if cc.MemoryMB, err = parseMemory(cj.Memory); err != nil {
			return cc, userErrorf("pail.json: %s's memory must be a size like 512MB.", name)
		}
		if cc.MemoryMB < minContainerMemoryMB {
			return cc, userErrorf("pail.json: %s asks for less memory than a microVM boots in. Ask for at least %dMB.", name, minContainerMemoryMB)
		}
	}

	if cj.Data != "" {
		data := path.Clean(cj.Data)
		if !strings.HasPrefix(cj.Data, "/") || data == "/" {
			return cc, userErrorf("pail.json: %s's data must be a folder's full path inside the container, like /data.", name)
		}
		for _, taken := range []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/etc", "/bin", "/sbin", "/lib", "/usr"} {
			if data == taken || strings.HasPrefix(data, taken+"/") {
				return cc, userErrorf("pail.json: %s can't keep its data at %s, which the system uses. Pick a folder of its own, like /data.", name, data)
			}
		}
		cc.Data = data
	}

	for key := range cj.Env {
		if !envNameRE.MatchString(key) {
			return cc, userErrorf("pail.json: %s has an environment variable called %q. Names are letters, numbers and underscores.", name, key)
		}
	}
	cc.Env = cj.Env

	if len(cj.Command) > 0 && string(cj.Command) != "null" {
		if err := json.Unmarshal(cj.Command, &cc.Command); err != nil {
			return cc, userErrorf("pail.json: %s's command must be a list of words, like [\"node\", \"server.js\"].", name)
		}
	}
	return cc, nil
}

// relPath cleans a path from pail.json. "" and "." mean the top.
func relPath(p string) (string, error) {
	for _, seg := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if seg == ".." {
			return "", userErrorf("path leaves the upload")
		}
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/"), nil
}

// CleanDir cleans the folder of a repo that a pail deploys from. "" means
// the top of the repo.
func CleanDir(dir string) (string, error) {
	return relPath(strings.TrimSpace(dir))
}

// Watched reads pail.json's watch list: the folders and files outside the
// pail's own that a push redeploys it for, as paths from the top of its repo.
// ok is false for a pail.json that can't be read.
func Watched(manifest []byte) (watch []string, ok bool) {
	var pj pailJSON
	if json.Unmarshal(manifest, &pj) != nil {
		return nil, false
	}
	for _, w := range pj.Watch {
		clean, err := relPath(w)
		if err != nil {
			return nil, false
		}
		watch = append(watch, clean)
	}
	return watch, true
}

// Target is what answers a path: a container, a function, or, with both
// empty, the deploy's files.
type Target struct {
	Container string
	Function  string
}

// Match finds what answers a path: the first route that fits. A deploy with
// no routes serves files for every path.
func (m *Manifest) Match(p string) (Target, bool) {
	if len(m.Routes) == 0 {
		return Target{}, true
	}
	for _, r := range m.Routes {
		if !routeFits(r.Path, p) {
			continue
		}
		switch kind, name, _ := strings.Cut(r.To, ":"); kind {
		case "container":
			return Target{Container: name}, true
		case "function":
			return Target{Function: name}, true
		}
		return Target{}, true
	}
	return Target{}, false
}

// routeFits says whether a route's path covers a request's. /api/* covers
// /api and everything under it; a path with no star covers only itself.
func routeFits(pattern, p string) bool {
	prefix, star := strings.CutSuffix(pattern, "*")
	if !star {
		return p == pattern || p == pattern+"/"
	}
	return strings.HasPrefix(p, prefix) || p == strings.TrimSuffix(prefix, "/")
}

// argv is what the container runs: the image's ENTRYPOINT, then pail.json's
// command or, without one, the image's CMD.
func (c Container) argv() []string {
	argv := append([]string{}, c.Entrypoint...)
	if len(c.Command) > 0 {
		return append(argv, c.Command...)
	}
	return append(argv, c.Cmd...)
}

// size says a container's size in words: "1 vCPU, 256MB".
func (c Container) size() string {
	cpus := strconv.Itoa(c.CPUs) + " vCPU"
	if c.CPUs != 1 {
		cpus += "s"
	}
	return cpus + ", " + strconv.Itoa(c.MemoryMB) + "MB"
}

// Package pails holds the pails themselves: their deploys in storage, the
// live pointer each request is served from, and the deploy pipeline that
// moves it.
package pails

import (
	"errors"
	"regexp"
	"time"
)

type Status string

const (
	StatusLive     Status = "live"
	StatusBuilding Status = "building"
	StatusFailed   Status = "failed"
	StatusOff      Status = "off"
)

type DeployState string

const (
	DeployBuilding DeployState = "building"
	DeployOK       DeployState = "ok"
	DeployFailed   DeployState = "failed"
)

// Pail is one hosted property as the API reports it.
type Pail struct {
	Name   string `json:"name"`
	Host   string `json:"host"`
	Status Status `json:"status"`
	// Source is where the latest deploy came from: "cli", "upload", or a
	// git host such as "github".
	Source string `json:"source"`
	// Repo and Revision are the repository and branch of a pail that
	// deploys from a git host.
	Repo     string `json:"repo,omitempty"`
	Revision string `json:"revision,omitempty"`
	// Dir is the folder of the repo the pail deploys from, when that isn't
	// the top: one pail of a repo that holds several.
	Dir string `json:"dir,omitempty"`
	// Hosts are the custom hostnames it also answers at.
	Hosts []string `json:"hosts"`
	// Containers are the containers of the deploy being served, and how
	// each is doing.
	Containers []ContainerStatus `json:"containers,omitempty"`
	// Functions are the functions of the deploy being served.
	Functions []FunctionStatus `json:"functions,omitempty"`
	// Routes say what answers each path of the deploy being served, when
	// its pail.json has server code.
	Routes []Route `json:"routes,omitempty"`
	// Serving is the ID of the deploy requests are answered from, or "".
	Serving   string    `json:"serving"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Deploy is the latest deploy, whatever its state.
	Deploy *Deploy `json:"deploy"`
}

// GitSource is the repo and branch a pail deploys from.
type GitSource struct {
	// Host is the kind of git host: "github", "forgejo", and so on.
	Host   string `json:"host"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Dir is the folder of the repo that holds the pail, or "" for the top.
	Dir string `json:"dir,omitempty"`
	// HookID and HookSecret are the webhook Pail added to the repo, so a
	// push deploys. They never leave the server.
	HookID     string `json:"hook_id,omitempty"`
	HookSecret string `json:"hook_secret,omitempty"`
}

// Deploy is one immutable upload of a pail.
type Deploy struct {
	ID         string      `json:"id"`
	State      DeployState `json:"state"`
	Source     string      `json:"source"`
	Label      string      `json:"label"`
	CreatedAt  time.Time   `json:"created_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	Files      int         `json:"files"`
	Bytes      int64       `json:"bytes"`
	Error      string      `json:"error,omitempty"`
}

// record is a pail as stored. Serving is the live pointer: moving a pail to
// another deploy is one write of this object.
type record struct {
	Name    string `json:"name"`
	Serving string `json:"serving"`
	// ServedAt is when the pointer last moved, by a deploy or a rollback.
	ServedAt time.Time `json:"served_at"`
	// Off says the pail was stopped: it keeps its deploys but answers nothing.
	Off bool `json:"off,omitempty"`
	// Hosts are the custom hostnames the pail answers at besides its own.
	Hosts []string `json:"hosts,omitempty"`
	// Git is the repo the pail deploys from, for a pail that came from a
	// git host.
	Git       *GitSource `json:"git,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Manifest lists what a deploy serves, so a request costs one object read.
type Manifest struct {
	// Root is the folder inside the deploy that is served, "" or "build/".
	Root string `json:"root"`
	// Fallback is served when a path matches no file (single-page apps).
	Fallback string `json:"fallback,omitempty"`
	// Files is keyed by path relative to Root.
	Files map[string]File `json:"files"`
	// Containers are the microVMs the deploy runs, by name.
	Containers map[string]Container `json:"containers,omitempty"`
	// Functions are the programs the deploy runs on demand, by name.
	Functions map[string]Function `json:"functions,omitempty"`
	// Routes say what answers each path. None means files answer them all.
	Routes []Route `json:"routes,omitempty"`
}

// Route sends the paths it covers to files, a function or a container. To is
// "static", "function:<name>" or "container:<name>".
type Route struct {
	Path string `json:"path"`
	To   string `json:"to"`
}

// Container is one container of a deploy: what pail.json asked for, and what
// the image built from its Dockerfile says about running it.
type Container struct {
	// Image is the registry image the container runs, for one that isn't
	// built from a Dockerfile, and Digest the exact contents that was
	// pulled for this deploy.
	Image  string `json:"image,omitempty"`
	Digest string `json:"digest,omitempty"`

	Port     int               `json:"port"`
	CPUs     int               `json:"cpus"`
	MemoryMB int               `json:"memory_mb"`
	Data     string            `json:"data,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	// Command, when set, runs in place of the image's CMD. The image's
	// ENTRYPOINT still goes in front of it, as it does with Docker.
	Command []string `json:"command,omitempty"`

	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	ImageEnv   []string `json:"image_env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
	User       string   `json:"user,omitempty"`
}

// Function is one function of a deploy: what pail.json asked for, and how
// its source turned out to be run.
type Function struct {
	Lang string `json:"lang"`
	// TimeoutMS is how long one request may take, and IdleMS how long a
	// copy waits for another before it stops.
	TimeoutMS int64 `json:"timeout_ms"`
	IdleMS    int64 `json:"idle_ms"`
	MemoryMB  int   `json:"memory_mb"`
	// Max is the most copies that run at once.
	Max int               `json:"max"`
	Env map[string]string `json:"env,omitempty"`

	// Argv is the program a request runs, and BaseEnv what the language and
	// its image set for it.
	Argv    []string `json:"argv"`
	BaseEnv []string `json:"base_env,omitempty"`
	// RootID names the root filesystem the function runs on.
	RootID string `json:"root_id"`
	// Snapshot says a copy is restored from a snapshot rather than booted.
	Snapshot bool `json:"snapshot"`
}

// FunctionStatus is a function as the API reports it.
type FunctionStatus struct {
	Name string `json:"name"`
	Lang string `json:"lang"`
	// Copies is how many microVMs of it are running now. None means it is
	// asleep, and costs nothing.
	Copies int `json:"copies"`
	Max    int `json:"max"`
}

// ContainerStatus is a container as the API reports it.
type ContainerStatus struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	// State is "running", "starting" or "stopped".
	State string `json:"state"`
}

type File struct {
	Size int64  `json:"size"`
	ETag string `json:"etag"`
	Type string `json:"type"`
}

var (
	ErrNoPail      = errors.New("no such pail")
	ErrNoDeploy    = errors.New("no such deploy")
	ErrNothingLive = errors.New("pail has no live deploy")
	ErrBadName     = errors.New("bad pail name")
	ErrNotArchive  = errors.New("not a .tar.gz or .zip")
	ErrBusy        = errors.New("pail is being removed")
	ErrBuilding    = errors.New("pail has a deploy running")
	ErrNotServable = errors.New("deploy didn't finish, so it can't be served")
	ErrOff         = errors.New("pail is off")
	ErrNoSource    = errors.New("pail has no finished deploy to redeploy")
	// ErrFunctionBusy means every copy of a function stayed busy for as
	// long as a request may wait.
	ErrFunctionBusy = errors.New("function is at its max")
	// ErrFunctionDown means a function has nothing to run on: this Pail
	// can't run microVMs, or the function's files are gone.
	ErrFunctionDown = errors.New("function can't run")
)

// ErrCantStart means a deploy's containers didn't come up, so the pail went
// on serving what it was serving.
type ErrCantStart struct{ Reason string }

func (e ErrCantStart) Error() string { return e.Reason }

// A pail name is one DNS label.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidName(name string) bool { return nameRE.MatchString(name) }

// Storage layout. Files sit where the handoff puts them; everything Pail
// writes about a pail sits under meta/ so it can never collide with them.
//
//	pails/<name>/deploys/<id>/...               the unpacked upload
//	meta/<name>/state.json                      record (the live pointer)
//	meta/<name>/deploys/<id>.json               Deploy
//	meta/<name>/deploys/<id>.log                log lines, one JSON per line
//	meta/<name>/deploys/<id>.manifest.json      Manifest
//	meta/<name>/deploys/<id>.rootfs.<c>.gz      a container's root filesystem
//	meta/<name>/deploys/<id>.fn.<f>.<part>.gz   a function: root, code, mem, state
const metaRoot = "meta/"

func metaPrefix(name string) string      { return metaRoot + name + "/" }
func stateKey(name string) string        { return metaPrefix(name) + "state.json" }
func deployMeta(name, id string) string  { return metaPrefix(name) + "deploys/" + id + "." }
func deployKey(name, id string) string   { return deployMeta(name, id) + "json" }
func logKey(name, id string) string      { return deployMeta(name, id) + "log" }
func manifestKey(name, id string) string { return deployMeta(name, id) + "manifest.json" }
func rootfsKey(name, id, container string) string {
	return deployMeta(name, id) + "rootfs." + container + ".gz"
}
func functionKey(name, id, function, part string) string {
	return deployMeta(name, id) + "fn." + function + "." + part + ".gz"
}
func pailFiles(name string) string       { return "pails/" + name + "/" }
func deployFiles(name, id string) string { return pailFiles(name) + "deploys/" + id + "/" }

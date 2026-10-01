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
	// Hosts are the custom hostnames it also answers at.
	Hosts []string `json:"hosts"`
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
)

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
const metaRoot = "meta/"

func metaPrefix(name string) string      { return metaRoot + name + "/" }
func stateKey(name string) string        { return metaPrefix(name) + "state.json" }
func deployMeta(name, id string) string  { return metaPrefix(name) + "deploys/" + id + "." }
func deployKey(name, id string) string   { return deployMeta(name, id) + "json" }
func logKey(name, id string) string      { return deployMeta(name, id) + "log" }
func manifestKey(name, id string) string { return deployMeta(name, id) + "manifest.json" }
func pailFiles(name string) string       { return "pails/" + name + "/" }
func deployFiles(name, id string) string { return pailFiles(name) + "deploys/" + id + "/" }

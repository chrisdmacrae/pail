// Package microvm runs Firecracker microVMs: the isolation Pail puts builds,
// containers and functions in. A microVM needs Linux with KVM; where there is
// none, Pail still serves static files and says why it can't do the rest.
package microvm

import (
	"log/slog"
	"sync"
)

// Config says where the pieces a microVM needs are on this machine.
type Config struct {
	// Firecracker is the firecracker binary.
	Firecracker string
	// Kernel is the guest kernel every microVM boots.
	Kernel string
	// Dir is local disk for root filesystems and work disks.
	Dir string
}

// Runner boots microVMs on this machine.
type Runner struct {
	cfg Config
	log *slog.Logger
	net network

	imagesMu sync.Mutex
	images   map[string]*imageEntry

	// build identifies this binary, which every image carries a copy of.
	buildOnce sync.Once
	build     string
	buildErr  error
}

func New(cfg Config, logger *slog.Logger) *Runner {
	return &Runner{cfg: cfg, log: logger, images: map[string]*imageEntry{}}
}

// Available says whether this machine can run microVMs, and if not, why, in
// words fit to show someone.
func (r *Runner) Available() (bool, string) {
	if r == nil {
		return false, "it was built without microVM support"
	}
	return r.available()
}

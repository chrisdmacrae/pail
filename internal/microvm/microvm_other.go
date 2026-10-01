//go:build !linux

package microvm

import (
	"context"
	"errors"
	"runtime"
)

// Everything below needs Linux. On other systems Pail compiles and runs, and
// reports that it can't run microVMs.

type network struct{}
type imageEntry struct{}

var errNotLinux = errors.New("microVMs need Linux with KVM; this is " + runtime.GOOS)

func (r *Runner) available() (bool, string) {
	return false, "microVMs need Linux with KVM, and this Pail runs on " + runtime.GOOS
}

func (r *Runner) BuildSite(context.Context, BuildRequest) (BuildResult, error) {
	return BuildResult{}, errNotLinux
}

// IsGuestInit is never true off Linux.
func IsGuestInit() bool { return false }

// GuestMain does nothing off Linux.
func GuestMain() {}

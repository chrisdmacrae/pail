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

func (r *Runner) BuildContainer(context.Context, ContainerBuild) (Built, error) {
	return Built{}, errNotLinux
}

func (r *Runner) PullContainer(context.Context, ContainerPull) (Built, error) {
	return Built{}, errNotLinux
}

func (r *Runner) Start(context.Context, MachineSpec) (Machine, error) {
	return nil, errNotLinux
}

func (r *Runner) BuildFunction(context.Context, FunctionBuild) (FunctionImage, error) {
	return FunctionImage{}, errNotLinux
}

func (r *Runner) StartFunction(context.Context, FunctionSpec) (FunctionCopy, error) {
	return nil, errNotLinux
}

// IsGuestInit is never true off Linux.
func IsGuestInit() bool { return false }

// GuestMain does nothing off Linux.
func GuestMain() {}

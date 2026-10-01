package microvm

import (
	"context"
	"io"
	"time"
)

// BuildRequest asks for a project to be built in a throwaway microVM.
type BuildRequest struct {
	// Fill puts the project's source in the folder it is given.
	Fill func(dir string) error
	// Dir, when set, is the folder inside the source that holds the project,
	// like apps/web: one project of a repo that holds several. The build
	// runs there, and may use what is above it, as a workspace does.
	Dir string
	// Static, when set, is the folder the build's output lands in, from
	// pail.json. Otherwise Pail looks in the usual places.
	Static string
	// Log receives the build's output, a line at a time.
	Log func(line string)
}

// BuildResult is a finished build.
type BuildResult struct {
	// Dir holds the built site on local disk.
	Dir string
	// Output is the folder inside the project the site was found in.
	Output string
	// Cleanup removes Dir and everything else the build left.
	Cleanup func()
}

// Image is a root filesystem a microVM can boot, made from a container image.
type Image struct {
	// Path is the ext4 file.
	Path string `json:"-"`
	// What the container image says about running it.
	Env        []string `json:"env"`
	Entrypoint []string `json:"entrypoint"`
	Cmd        []string `json:"cmd"`
	WorkingDir string   `json:"working_dir"`
	User       string   `json:"user,omitempty"`
}

// ContainerBuild asks for a Dockerfile to be built in a throwaway microVM.
type ContainerBuild struct {
	// Fill puts the project's source in the folder it is given.
	Fill func(dir string) error
	// Dockerfile and Context are paths inside the source.
	Dockerfile string
	Context    string
	// Log receives the build's output, a line at a time.
	Log func(line string)
}

// ContainerPull asks for an image from a registry to be turned into a root
// filesystem, with nothing built.
type ContainerPull struct {
	// Ref names the image, as Docker would: nginx:1.27, ghcr.io/owner/app.
	Ref string
	// Unless, when set, is the digest of the image the caller already has.
	// If the registry still serves that one, nothing is fetched.
	Unless string
	// Log receives progress, a line at a time.
	Log func(line string)
}

// Built is a container image turned into a root filesystem.
type Built struct {
	// Image is nil when a pull found the image the caller already has.
	Image *Image
	// Digest identifies a pulled image's contents.
	Digest string
	// Cleanup removes the root filesystem and everything else the build left.
	Cleanup func()
}

// MachineSpec is a long-lived microVM: a container.
type MachineSpec struct {
	// Dir is a folder of the machine's own, for its disk and its files.
	// It is emptied first and removed when the machine stops.
	Dir string
	// Rootfs is the root filesystem to boot. The machine runs on a copy, so
	// what it writes there lasts until it stops and no longer.
	Rootfs string
	// Data, when set, is a volume that outlasts the machine, mounted at
	// DataPath. It is made, DataMB big, the first time it is used.
	Data     string
	DataPath string
	DataMB   int64

	Argv []string
	Env  []string
	// Dir the command runs in, and the user it runs as, as an image names
	// them: "", "app", "1000:1000".
	WorkingDir string
	User       string
	Hostname   string

	// Group, when set, names the machines that may reach each other: those
	// started with the same Group, each known to the others by its
	// Hostname. Peers is every Hostname in the group, this one's included.
	// A machine in no group is reached by Pail and by nothing else.
	Group string
	Peers []string
	// LAN lets the machine reach the home network, which no machine can
	// otherwise: for a pail the person running Pail has named as trusted.
	LAN bool

	VCPUs int
	MemMB int
	// Log receives what the machine prints, a line at a time.
	Log func(line string)
}

// Machine is a running microVM.
type Machine interface {
	// Addr is where a port inside the machine is reached from this host.
	Addr(port int) string
	// Done closes when the machine has stopped, whoever stopped it.
	Done() <-chan struct{}
	// Stop asks what's running to finish, gives it a moment, and stops the
	// machine. It returns once the machine is gone.
	Stop()
}

// FunctionBuild asks for a function's source to be made ready to run: built
// in a throwaway microVM if its language needs it, then booted once and
// snapshotted, so that a copy can be restored in a moment.
type FunctionBuild struct {
	// Fill puts the function's source in the folder it is given.
	Fill func(dir string) error
	// BuildImage and Script build the source: Script runs in the source's
	// folder, in a microVM made from BuildImage. An empty Script means
	// there is nothing to build.
	BuildImage string
	Script     string
	// RunImage is the image the function runs in.
	RunImage string
	// MemMB is the memory each copy gets.
	MemMB int
	// Warm, when set, is a command run once in the function's folder before
	// the snapshot is taken, with WarmEnv, so that what the function needs
	// from disk is already in the memory a copy wakes up with.
	Warm    []string
	WarmEnv []string
	// Dir is where the function's files are left.
	Dir string
	// Log receives the build's output, a line at a time.
	Log func(line string)
}

// FunctionImage is a function ready to run: what BuildFunction leaves.
type FunctionImage struct {
	// Root is the root filesystem and Code the function's own files, both
	// read-only when it runs.
	Root, Code string
	// RootID names Root's contents: two functions with the same RootID have
	// the same root filesystem.
	RootID string
	// Mem and State are the snapshot a copy is restored from. They are
	// empty if this machine couldn't take one; copies then boot from cold.
	Mem, State string
	// Env is the run image's own environment.
	Env []string
}

// FunctionSpec is one copy of a function to start.
type FunctionSpec struct {
	// Dir is a folder of the copy's own. It is removed when the copy stops.
	Dir   string
	Image FunctionImage
	MemMB int
	// Log receives what the copy's microVM itself prints.
	Log func(line string)
}

// FunctionCall is one request for a function: a program to run to its end.
type FunctionCall struct {
	Argv []string
	Env  []string
	// Dir is where the program runs, inside the function's own files.
	Dir string
	// Body is the program's standard input, BodyLen bytes of it.
	Body    io.Reader
	BodyLen int64
	// Timeout is how long the program may run.
	Timeout time.Duration
	// MaxOutput is the most the program may write to standard output.
	MaxOutput int64
	// Stderr receives what the program writes to standard error, a line at
	// a time.
	Stderr func(line string)
}

// FunctionResult is how a call ended.
type FunctionResult struct {
	// Output is what the program wrote to standard output.
	Output   []byte
	ExitCode int
	// Signal names the signal that ended the program, if one did.
	Signal      string
	TimedOut    bool
	OutOfMemory bool
	// TooBig says the program wrote more than MaxOutput and was stopped.
	TooBig bool
}

// FunctionCopy is one running microVM of a function. It runs one call at a
// time.
type FunctionCopy interface {
	Call(ctx context.Context, call FunctionCall) (FunctionResult, error)
	// Done closes when the copy has stopped.
	Done() <-chan struct{}
	Stop()
}

// Machines is what runs builds and containers. Runner is the real one.
type Machines interface {
	Available() (bool, string)
	BuildSite(ctx context.Context, req BuildRequest) (BuildResult, error)
	BuildContainer(ctx context.Context, req ContainerBuild) (Built, error)
	PullContainer(ctx context.Context, req ContainerPull) (Built, error)
	Start(ctx context.Context, spec MachineSpec) (Machine, error)
	BuildFunction(ctx context.Context, req FunctionBuild) (FunctionImage, error)
	StartFunction(ctx context.Context, spec FunctionSpec) (FunctionCopy, error)
}

package microvm

import "context"

// BuildRequest asks for a project to be built in a throwaway microVM.
type BuildRequest struct {
	// Fill puts the project's source in the folder it is given.
	Fill func(dir string) error
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

// Machines is what runs builds and containers. Runner is the real one.
type Machines interface {
	Available() (bool, string)
	BuildSite(ctx context.Context, req BuildRequest) (BuildResult, error)
	BuildContainer(ctx context.Context, req ContainerBuild) (Built, error)
	PullContainer(ctx context.Context, req ContainerPull) (Built, error)
	Start(ctx context.Context, spec MachineSpec) (Machine, error)
}

package microvm

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

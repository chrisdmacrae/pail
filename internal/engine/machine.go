package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chrisdmacrae/pail/internal/microvm"
)

// machine is a long-lived container.
type machine struct {
	r    *Runner
	id   string
	ip   string
	gone chan struct{}
	stop sync.Once
}

// Start runs a container from the image a deploy keeps. It returns as soon
// as the container is running; what's inside takes a moment longer.
func (r *Runner) Start(ctx context.Context, spec microvm.MachineSpec) (microvm.Machine, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("nothing to run")
	}
	ref, err := r.imageIn(ctx, spec.Rootfs)
	if err != nil {
		return nil, err
	}
	cfg := containerConfig{
		Image: ref, Entrypoint: spec.Argv[:1], Cmd: spec.Argv[1:], Env: spec.Env,
		WorkingDir: spec.WorkingDir, User: spec.User, Hostname: spec.Hostname, Labels: r.labels(),
		HostConfig: hostConfig{
			Memory: int64(spec.MemMB) << 20, NanoCPUs: int64(spec.VCPUs) * 1e9, NetworkMode: r.cfg.Network,
		},
	}
	if spec.Data != "" {
		volume, err := r.volume(ctx, spec.Data)
		if err != nil {
			return nil, err
		}
		cfg.HostConfig.Binds = []string{volume + ":" + spec.DataPath}
	}

	id, err := r.api.create(ctx, r.cfg.Network+"-run."+filepath.Base(spec.Dir), cfg)
	if err != nil {
		return nil, err
	}
	// discard removes the container, and its group's network if it was the
	// last one on it.
	discard := func() {
		r.discard(id)
		if spec.Group != "" {
			r.leaveGroup(spec.Group)
		}
	}
	if spec.Group != "" {
		if err := r.joinGroup(ctx, id, spec.Group, spec.Hostname); err != nil {
			discard()
			return nil, err
		}
	}
	if err := r.api.start(ctx, id); err != nil {
		discard()
		return nil, err
	}
	ip, err := r.address(ctx, id)
	if err != nil {
		discard()
		return nil, err
	}
	m := &machine{r: r, id: id, ip: ip, gone: make(chan struct{})}
	// The container outlives the call that started it.
	go r.api.logs(context.Background(), id, spec.Log)
	go func() {
		r.ended(id)
		discard()
		close(m.gone)
	}()
	return m, nil
}

// A group is a set of containers that may find each other by name: a pail's
// containers. Every container is on Pail's network, where Pail reaches it.
// A group has a network of its own beside that one, where each of its
// containers answers to its name. Another pail's containers aren't on it, so
// two pails can each have a "db".

// groupNetwork names the engine's network for a group.
func (r *Runner) groupNetwork(group string) string {
	return r.cfg.Network + "-net." + group
}

// joinGroup puts a container that hasn't started on its group's network,
// making the network for the first of them.
func (r *Runner) joinGroup(ctx context.Context, id, group, name string) error {
	r.groupMu.Lock()
	defer r.groupMu.Unlock()
	network := r.groupNetwork(group)
	err := r.api.call(ctx, "GET", "/networks/"+network, nil, nil, nil)
	if notFound(err) {
		err = r.api.call(ctx, "POST", "/networks/create", nil, map[string]any{"Name": network, "Labels": r.labels()}, nil)
	}
	if err == nil {
		err = r.api.connect(ctx, network, id, name)
	}
	if err != nil {
		return fmt.Errorf("the network %s shares with the pail's other containers: %w", name, err)
	}
	return nil
}

// leaveGroup removes a group's network once none of its containers is left.
func (r *Runner) leaveGroup(group string) {
	r.groupMu.Lock()
	defer r.groupMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The engine won't remove a network that has a container on it, which
	// is the answer wanted while the group has others running.
	r.api.call(ctx, "DELETE", "/networks/"+r.groupNetwork(group), nil, nil, nil)
}

func (m *machine) Addr(port int) string { return net.JoinHostPort(m.ip, strconv.Itoa(port)) }

func (m *machine) Done() <-chan struct{} { return m.gone }

func (m *machine) Stop() {
	m.stop.Do(func() {
		grace := int(microvm.StopGrace / time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), microvm.StopGrace+15*time.Second)
		defer cancel()
		if err := m.r.api.stop(ctx, m.id, grace); err != nil {
			m.r.discard(m.id)
		}
	})
	<-m.gone
}

// A data volume is one of the engine's own. Pail leaves an empty file where
// a microVM's volume would be on its disk, to stand for it: when a pail is
// removed its files go, and a volume whose file has gone goes after it.

// volume returns the engine's volume for the data kept at file, making it
// if this is its first use.
func (r *Runner) volume(ctx context.Context, file string) (string, error) {
	id, err := r.dataID()
	if err != nil {
		return "", err
	}
	marker, err := filepath.Rel(r.cfg.Dir, file)
	if err != nil {
		return "", err
	}
	// volumes/<pail>/<container>.ext4 is the volume <network>-vol.<pail>.<container>.
	parts := strings.Split(strings.TrimSuffix(filepath.ToSlash(marker), filepath.Ext(marker)), "/")
	volume := r.cfg.Network + "-vol." + strings.Join(parts[1:], ".")

	var existing struct {
		Labels map[string]string `json:"Labels"`
	}
	err = r.api.call(ctx, "GET", "/volumes/"+volume, nil, nil, &existing)
	if _, gone := os.Stat(file); err == nil && gone != nil && existing.Labels[labelData] == id {
		// A volume without its file belonged to a pail that was removed: a
		// new pail of the same name starts with nothing.
		if err := r.api.call(ctx, "DELETE", "/volumes/"+volume, nil, nil, nil); err != nil {
			return "", fmt.Errorf("remove the volume a removed pail left: %w", err)
		}
		err = &apiError{Status: 404}
	}
	// The file goes first: a volume without one is taken for a removed
	// pail's, and tidied away.
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		return "", err
	}
	if notFound(err) {
		err = r.api.call(ctx, "POST", "/volumes/create", nil, map[string]any{
			"Name": volume, "Labels": map[string]string{labelInstance: r.cfg.Network, labelData: id, labelMarker: filepath.ToSlash(marker)},
		}, nil)
	}
	if err != nil {
		return "", fmt.Errorf("make the data volume: %w", err)
	}
	return volume, nil
}

// dataID names this Pail's disk. It is made once and kept there, so a Pail
// given an empty disk doesn't take another's volumes for its own, or for
// ones to tidy away.
func (r *Runner) dataID() (string, error) {
	file := filepath.Join(r.cfg.Dir, "engine-id")
	if raw, err := os.ReadFile(file); err == nil && len(raw) > 0 {
		return strings.TrimSpace(string(raw)), nil
	}
	if err := os.MkdirAll(r.cfg.Dir, 0o755); err != nil {
		return "", err
	}
	id := randomHex(8)
	return id, os.WriteFile(file, []byte(id+"\n"), 0o644)
}

// sweepVolumes removes the volumes of pails that were removed.
func (r *Runner) sweepVolumes(ctx context.Context) {
	raw, err := os.ReadFile(filepath.Join(r.cfg.Dir, "engine-id"))
	if err != nil {
		return // this disk has made no volumes
	}
	var listed struct {
		Volumes []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if err := r.api.call(ctx, "GET", "/volumes", filter(labelData+"="+strings.TrimSpace(string(raw))), nil, &listed); err != nil {
		r.log.Warn("list data volumes", "err", err)
		return
	}
	for _, v := range listed.Volumes {
		marker := v.Labels[labelMarker]
		if marker == "" || v.Labels[labelInstance] != r.cfg.Network {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.cfg.Dir, filepath.FromSlash(marker))); os.IsNotExist(err) {
			if err := r.api.call(ctx, "DELETE", "/volumes/"+v.Name, nil, nil, nil); err != nil {
				r.log.Warn("remove a removed pail's data volume", "volume", v.Name, "err", err)
			}
		}
	}
}

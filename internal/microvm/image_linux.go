//go:build linux

package microvm

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// Image is a root filesystem a microVM can boot, made from a container image.
type Image struct {
	// Path is the ext4 file.
	Path string `json:"-"`
	// What the container image says about running it.
	Env        []string `json:"env"`
	Entrypoint []string `json:"entrypoint"`
	Cmd        []string `json:"cmd"`
	WorkingDir string   `json:"working_dir"`
}

// imageEntry lets one image be made once, however many ask for it at once.
type imageEntry struct {
	once sync.Once
	img  *Image
	err  error
}

// The resolvers a guest uses. It can't reach the home network, so it can't
// use the home resolver either.
const guestResolvConf = "nameserver 1.1.1.1\nnameserver 8.8.8.8\n"

// Image returns the root filesystem for a container image reference, pulling
// and converting it the first time. Later calls reuse the file.
func (r *Runner) Image(ctx context.Context, ref string, log func(string)) (*Image, error) {
	r.imagesMu.Lock()
	e := r.images[ref]
	if e == nil {
		e = &imageEntry{}
		r.images[ref] = e
	}
	r.imagesMu.Unlock()

	e.once.Do(func() { e.img, e.err = r.makeImage(ctx, ref, log) })
	if e.err != nil {
		// Let the next ask try again rather than remember a network blip.
		r.imagesMu.Lock()
		delete(r.images, ref)
		r.imagesMu.Unlock()
	}
	return e.img, e.err
}

func (r *Runner) makeImage(ctx context.Context, ref string, log func(string)) (*Image, error) {
	// An image holds a copy of this binary as its init, so one made by
	// another build of Pail is stale: its name carries which build made it.
	build, err := r.buildID()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(ref))
	base := filepath.Join(r.cfg.Dir, "images", build+"-"+hex.EncodeToString(sum[:8]))
	img := &Image{Path: base + ".ext4"}
	if meta, err := os.ReadFile(base + ".json"); err == nil {
		if _, err := os.Stat(img.Path); err == nil && json.Unmarshal(meta, img) == nil {
			return img, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return nil, err
	}
	// Clear out what earlier builds of Pail left.
	if old, err := os.ReadDir(filepath.Dir(base)); err == nil {
		for _, entry := range old {
			if !strings.HasPrefix(entry.Name(), build+"-") {
				os.RemoveAll(filepath.Join(filepath.Dir(base), entry.Name()))
			}
		}
	}

	log("pulling " + ref)
	pulled, err := crane.Pull(ref, crane.WithContext(ctx), crane.WithPlatform(&v1.Platform{OS: "linux", Architecture: runtime.GOARCH}))
	if err != nil {
		return nil, fmt.Errorf("can't pull %s: %w", ref, err)
	}
	cfg, err := pulled.ConfigFile()
	if err != nil {
		return nil, err
	}
	img.Env, img.Entrypoint, img.Cmd, img.WorkingDir = cfg.Config.Env, cfg.Config.Entrypoint, cfg.Config.Cmd, cfg.Config.WorkingDir

	root := base + ".root"
	os.RemoveAll(root)
	defer os.RemoveAll(root)
	flat := mutate.Extract(pulled)
	err = untar(flat, root)
	flat.Close()
	if err != nil {
		return nil, fmt.Errorf("can't unpack %s: %w", ref, err)
	}
	if err := prepareRoot(root); err != nil {
		return nil, err
	}
	if err := makeExt4(ctx, root, img.Path+".tmp", 0); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(img)
	if err := os.WriteFile(base+".json", meta, 0o644); err != nil {
		return nil, err
	}
	return img, os.Rename(img.Path+".tmp", img.Path)
}

// buildID names this build of Pail, from the contents of its binary.
func (r *Runner) buildID() (string, error) {
	r.buildOnce.Do(func() {
		self, err := os.Executable()
		if err != nil {
			r.buildErr = err
			return
		}
		f, err := os.Open(self)
		if err != nil {
			r.buildErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			r.buildErr = err
			return
		}
		r.build = hex.EncodeToString(h.Sum(nil)[:6])
	})
	return r.build, r.buildErr
}

// prepareRoot adds what every Pail guest needs to an unpacked image: Pail
// itself as init, the mount points init uses, and name resolution.
func prepareRoot(root string) error {
	for _, dir := range []string{"proc", "sys", "dev", "tmp", "run", "work", "etc"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return err
		}
	}
	// The guest runs the same binary the host does, so it always matches.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := copyFile(self, filepath.Join(root, guestInit), 0o755); err != nil {
		return err
	}
	// An image's resolv.conf is often a link to somewhere that isn't there.
	resolv := filepath.Join(root, "etc", "resolv.conf")
	os.Remove(resolv)
	if err := os.WriteFile(resolv, []byte(guestResolvConf), 0o644); err != nil {
		return err
	}
	hosts := filepath.Join(root, "etc", "hosts")
	if _, err := os.Lstat(hosts); errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(hosts, []byte("127.0.0.1 localhost\n::1 localhost\n"), 0o644)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// untar unpacks a flattened image into dir, keeping owners, modes, links and
// device nodes, which is why Pail does this as root.
func untar(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean("/" + hdr.Name)
		if clean == "/" {
			continue
		}
		target := filepath.Join(dir, clean)
		mode := os.FileMode(hdr.Mode).Perm()
		// Something may already sit where this entry goes.
		if hdr.Typeflag != tar.TypeDir {
			os.MkdirAll(filepath.Dir(target), 0o755)
			os.Remove(target)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.Link(filepath.Join(dir, filepath.Clean("/"+hdr.Linkname)), target); err != nil {
				return err
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			kind := map[byte]uint32{tar.TypeChar: syscall.S_IFCHR, tar.TypeBlock: syscall.S_IFBLK, tar.TypeFifo: syscall.S_IFIFO}[hdr.Typeflag]
			dev := int(hdr.Devmajor)<<8 | int(hdr.Devminor)
			// Inside an unprivileged container Pail may not make device
			// nodes. Images rarely carry any, and a guest gets its own /dev.
			if err := syscall.Mknod(target, kind|uint32(mode), dev); errors.Is(err, syscall.EPERM) {
				continue
			} else if err != nil {
				return err
			}
		default:
			continue
		}
		if err := os.Lchown(target, hdr.Uid, hdr.Gid); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeSymlink {
			// Chmod after chown: chown clears setuid bits.
			if err := os.Chmod(target, os.FileMode(hdr.Mode).Perm()|specialBits(hdr.Mode)); err != nil {
				return err
			}
		}
	}
}

func specialBits(mode int64) os.FileMode {
	var m os.FileMode
	if mode&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m
}

// makeExt4 turns a folder into an ext4 filesystem in a file, with no mount
// involved. sizeMB of 0 means just big enough for the folder.
func makeExt4(ctx context.Context, dir, file string, sizeMB int64) error {
	if sizeMB == 0 {
		var bytes int64
		filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil {
				if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
					bytes += info.Size()
				}
				bytes += 4096 // each entry costs an inode and some directory
			}
			return nil
		})
		sizeMB = bytes/(1<<20)*13/10 + 64
	}
	os.Remove(file)
	out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-d", dir, "-L", "pail", file, fmt.Sprintf("%dM", sizeMB)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.ext4: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

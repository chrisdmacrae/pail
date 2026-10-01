package engine

import (
	"archive/tar"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Files go in and out of a container as tar archives, since Pail and the
// engine need not see the same disk.

// tarDir writes a folder's contents to w, as entries under prefix. Folders
// above them are written too, so the archive unpacks anywhere.
func tarDir(w *tar.Writer, dir, prefix string) error {
	prefix = strings.Trim(prefix, "/")
	for at := ""; at != prefix; {
		rest := strings.TrimPrefix(strings.TrimPrefix(prefix, at), "/")
		at = path.Join(at, strings.SplitN(rest, "/", 2)[0])
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: at + "/", Mode: 0o755}); err != nil {
			return err
		}
	}
	return filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, file)
		if err != nil || rel == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		switch {
		case info.IsDir():
			return w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(info.Mode().Perm())})
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(file)
			if err != nil {
				return err
			}
			return w.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777})
		case !info.Mode().IsRegular():
			return nil
		}
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}

// archive returns a tar archive that write fills in as it is read.
func archive(write func(w *tar.Writer) error) io.ReadCloser {
	r, w := io.Pipe()
	go func() {
		tw := tar.NewWriter(w)
		err := write(tw)
		if err == nil {
			err = tw.Close()
		}
		w.CloseWithError(err)
	}()
	return r
}

// untar unpacks an archive into dir: its folders, its files, and the links
// that stay inside it. Nothing lands outside dir.
func untar(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	inside := func(name string) (string, bool) {
		target := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+name)))
		return target, target != dir
	}
	tr := tar.NewReader(r)
	for {
		head, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := inside(head.Name)
		if !ok {
			continue
		}
		switch head.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			perm := fs.FileMode(0o644)
			if head.Mode&0o111 != 0 {
				perm = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Only a link to somewhere else in the archive is kept.
			if filepath.IsAbs(head.Linkname) {
				continue
			}
			to := filepath.Join(filepath.Dir(target), filepath.FromSlash(head.Linkname))
			if rel, err := filepath.Rel(dir, to); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(head.Linkname, target); err != nil {
				return err
			}
		}
	}
}

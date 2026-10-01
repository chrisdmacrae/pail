package pails

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"strings"
)

type archiveFormat int

const (
	formatTarGz archiveFormat = iota
	formatZip
)

// sniffArchive tells a .tar.gz from a .zip by its first bytes.
func sniffArchive(file string) (archiveFormat, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return formatTarGz, nil
	case n == 4 && string(magic[:2]) == "PK":
		return formatZip, nil
	}
	return 0, ErrNotArchive
}

// walkArchive calls fn for every regular file in the archive, in order. r is
// only valid until fn returns.
func walkArchive(file string, format archiveFormat, fn func(name string, size int64, r io.Reader) error) error {
	return walkArchiveModes(file, format, func(name string, size int64, _ fs.FileMode, r io.Reader) error {
		return fn(name, size, r)
	})
}

// walkArchiveModes is walkArchive for a caller that needs to know which
// files may be run.
func walkArchiveModes(file string, format archiveFormat, fn func(name string, size int64, mode fs.FileMode, r io.Reader) error) error {
	if format == formatZip {
		zr, err := zip.OpenReader(file)
		if err != nil {
			return userErrorf("That .zip can't be read: %v.", err)
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if !zf.Mode().IsRegular() {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return userErrorf("That .zip can't be read: %v.", err)
			}
			err = fn(zf.Name, int64(zf.UncompressedSize64), zf.Mode().Perm(), rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return userErrorf("That .tar.gz can't be read: %v.", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return userErrorf("That .tar.gz can't be read: %v.", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := fn(hdr.Name, hdr.Size, fs.FileMode(hdr.Mode).Perm(), tr); err != nil {
			return err
		}
	}
}

// cleanName turns an archive entry's name into a path inside the deploy.
// skip is true for entries Pail leaves out.
func cleanName(name string) (clean string, skip bool, err error) {
	name = strings.ReplaceAll(name, `\`, "/")
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", false, userErrorf("The archive has a path that climbs out of it (%s). Pack the folder itself.", name)
		}
	}
	clean = strings.TrimPrefix(path.Clean("/"+name), "/")
	if clean == "" || !fs.ValidPath(clean) {
		return "", true, nil
	}
	// What macOS adds to folders, zips and tars.
	base := path.Base(clean)
	if strings.HasPrefix(clean, "__MACOSX/") || base == ".DS_Store" || strings.HasPrefix(base, "._") {
		return "", true, nil
	}
	return clean, false, nil
}

// Types Go's built-in table doesn't have, or that differ between hosts.
var contentTypes = map[string]string{
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".map":         "application/json",
	".md":          "text/markdown; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".mp3":         "audio/mpeg",
	".mp4":         "video/mp4",
	".otf":         "font/otf",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
	".webm":        "video/webm",
	".webmanifest": "application/manifest+json",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".xml":         "application/xml",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := contentTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

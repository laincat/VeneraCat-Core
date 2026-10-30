package magpieplugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ErrNotAnArchive means the bytes are neither a zip nor a gzipped tar.
var ErrNotAnArchive = errors.New("magpieplugin: not a zip or tar.gz package")

// ReadArchive turns a downloaded package into the flat file table NewBundle
// takes. A package is published either as a zip or as an npm tarball (tar.gz),
// and both usually carry a single top-level directory, which is stripped: the
// files are the same package either way, and the directory name is an accident
// of how it was packed.
//
// Symlinks, device entries and absolute paths are refused rather than followed:
// an installer must not be able to write outside the package, and a link has no
// meaning once the files live in memory.
func ReadArchive(data []byte) (map[string][]byte, error) {
	switch {
	case len(data) >= 4 && bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return readZip(data)
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		return readTarGz(data)
	default:
		return nil, ErrNotAnArchive
	}
}

func readZip(data []byte) (map[string][]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("magpieplugin: reading zip: %w", err)
	}
	files := map[string][]byte{}
	var total int
	for _, f := range r.File {
		mode := f.Mode()
		if mode.IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalidPath, f.Name)
		}
		if f.UncompressedSize64 > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes", ErrLimit, f.Name, f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("magpieplugin: opening %s: %w", f.Name, err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("magpieplugin: reading %s: %w", f.Name, err)
		}
		if err := addPackaged(files, f.Name, body, &total); err != nil {
			return nil, err
		}
	}
	return stripCommonDir(files), nil
}

func readTarGz(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("magpieplugin: reading gzip: %w", err)
	}
	defer gz.Close()
	r := tar.NewReader(gz)
	files := map[string][]byte{}
	var total int
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("magpieplugin: reading tar: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalidPath, h.Name)
		}
		if h.Size > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes", ErrLimit, h.Name, h.Size)
		}
		body, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("magpieplugin: reading %s: %w", h.Name, err)
		}
		if err := addPackaged(files, h.Name, body, &total); err != nil {
			return nil, err
		}
	}
	return stripCommonDir(files), nil
}

// addPackaged records one entry, applying the same path and size rules the
// bundle applies, so an archive cannot smuggle in what a file table could not.
func addPackaged(files map[string][]byte, name string, body []byte, total *int) error {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || clean == "" {
		return nil
	}
	if strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("%w: %s", ErrInvalidPath, name)
	}
	if len(body) > MaxFileBytes {
		return fmt.Errorf("%w: %s is %d bytes", ErrLimit, clean, len(body))
	}
	*total += len(body)
	if *total > MaxTotalBytes {
		return fmt.Errorf("%w: %d bytes in total", ErrLimit, *total)
	}
	if len(files) >= MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrLimit, MaxFiles)
	}
	files[clean] = body
	return nil
}

// stripCommonDir removes one enclosing directory when every file shares it:
// both npm tarballs and "zip this folder" downloads have exactly one, and the
// bundle wants package-relative paths.
func stripCommonDir(files map[string][]byte) map[string][]byte {
	if len(files) == 0 {
		return files
	}
	var prefix string
	first := true
	for name := range files {
		dir, _, ok := strings.Cut(name, "/")
		if !ok {
			return files // a file at the root: nothing to strip
		}
		if first {
			prefix, first = dir, false
			continue
		}
		if dir != prefix {
			return files
		}
	}
	out := make(map[string][]byte, len(files))
	for name, body := range files {
		out[strings.TrimPrefix(name, prefix+"/")] = body
	}
	return out
}

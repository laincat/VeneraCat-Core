package service

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// MaxPackageBytes bounds the uncompressed size of a service package. A service
// is a few hundred lines of JavaScript and a manifest; a package that needs
// more than this is not one, and expanding it first would be the mistake.
const MaxPackageBytes = 8 << 20

// MaxPackageFiles bounds how many files a package may hold.
const MaxPackageFiles = 64

// ReadPackageZip reads a service package from a zip archive.
//
// The archive must hold ManifestFileName at its root and the file the manifest
// names; anything that would land outside the package, or that sits under a
// directory that only looks like one, is refused. The uncompressed size and the
// number of files are both bounded, because a zip can claim anything about its
// sizes and expanding it before checking is how a host runs out of memory.
func ReadPackageZip(data []byte) (*Manifest, string, map[string][]byte, error) {
	if len(data) == 0 {
		return nil, "", nil, errors.New("service: empty package")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", nil, fmt.Errorf("service: not a zip archive: %w", err)
	}
	if len(reader.File) > MaxPackageFiles {
		return nil, "", nil, fmt.Errorf("service: package holds %d files, over the %d limit", len(reader.File), MaxPackageFiles)
	}
	files := map[string][]byte{}
	total := 0
	for _, f := range reader.File {
		name := strings.TrimPrefix(f.Name, "./")
		if err := validEntry(name); err != nil {
			return nil, "", nil, fmt.Errorf("service: package file %q: %w", f.Name, err)
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > MaxPackageBytes {
			return nil, "", nil, fmt.Errorf("service: %s claims %d bytes, over the %d limit", name, f.UncompressedSize64, MaxPackageBytes)
		}
		content, err := readZipFile(f)
		if err != nil {
			return nil, "", nil, fmt.Errorf("service: %s: %w", name, err)
		}
		total += len(content)
		if total > MaxPackageBytes {
			return nil, "", nil, fmt.Errorf("service: package expands to more than %d bytes", MaxPackageBytes)
		}
		files[name] = content
	}
	manifestData, ok := files[ManifestFileName]
	if !ok {
		return nil, "", nil, fmt.Errorf("service: package has no %s", ManifestFileName)
	}
	m, err := ParseManifest(manifestData)
	if err != nil {
		return nil, "", nil, err
	}
	entry, ok := files[m.Entry]
	if !ok {
		return nil, "", nil, fmt.Errorf("service: package has no entry %q", m.Entry)
	}
	return m, string(entry), files, nil
}

// readZipFile reads one file, refusing one that expands past its declared size
// or past the package limit. A zip may lie about UncompressedSize64, so the
// limit is applied to what is actually read.
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	limited := io.LimitReader(rc, MaxPackageBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxPackageBytes {
		return nil, fmt.Errorf("expands past the %d byte limit", MaxPackageBytes)
	}
	return data, nil
}

// ValidFilePath reports whether name may be written as a file of a service
// package. An installer calls it before it turns a name from an archive into a
// path on disk; ReadPackageZip applies it already, and a host that keeps the
// archive around applies it again.
func ValidFilePath(name string) error {
	if err := validEntry(name); err != nil {
		return err
	}
	if path.Clean(name) != name {
		return fmt.Errorf("service: package file %q is not a clean path", name)
	}
	return nil
}

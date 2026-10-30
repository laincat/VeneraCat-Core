package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestFileName is the file a service package keeps its manifest in.
const ManifestFileName = "service.json"

// MaxEntryBytes bounds the entry file of a service package.
const MaxEntryBytes = 4 << 20

// LoadPackage loads the service package in dir into rt.
//
// A package is a directory with a manifest next to the file the manifest
// names. The manifest is parsed and checked first, so a package whose
// manifest is wrong never reaches the runtime, and the entry is read from
// inside the package directory only.
func LoadPackage(ctx context.Context, rt *Runtime, dir string) (*Manifest, error) {
	if rt == nil {
		return nil, errors.New("service: no runtime")
	}
	m, source, err := ReadPackage(dir)
	if err != nil {
		return nil, err
	}
	if err := rt.Load(ctx, m, source); err != nil {
		return nil, err
	}
	return m, nil
}

// ReadPackage reads the manifest and the entry of the service package in dir.
//
// A host that wants to know the service identity before it builds its own
// callbacks uses this: the manifest says who the service is, and the entry is
// the source to hand to Runtime.Load.
func ReadPackage(dir string) (*Manifest, string, error) {
	manifestData, err := readLimited(filepath.Join(dir, ManifestFileName), MaxManifestBytes)
	if err != nil {
		return nil, "", fmt.Errorf("service: manifest: %w", err)
	}
	m, err := ParseManifest(manifestData)
	if err != nil {
		return nil, "", err
	}
	entryPath, err := resolveEntry(dir, m.Entry)
	if err != nil {
		return nil, "", err
	}
	source, err := readLimited(entryPath, MaxEntryBytes)
	if err != nil {
		return nil, "", fmt.Errorf("service: entry: %w", err)
	}
	return m, string(source), nil
}

// resolveEntry turns the entry of a manifest into a path inside dir.
//
// The manifest already refuses an entry that leaves the package, but the path
// is checked again here: this is the point where a name from a file on disk
// becomes a file the host opens, and the check belongs here.
func resolveEntry(dir, entry string) (string, error) {
	if err := validEntry(entry); err != nil {
		return "", err
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, filepath.FromSlash(entry))
	rel, err := filepath.Rel(base, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("service: entry %q leaves the package", entry)
	}
	return full, nil
}

// readLimited reads a file and refuses one that is larger than the limit.
func readLimited(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("%s is %d bytes, over the %d limit", path, info.Size(), limit)
	}
	return os.ReadFile(path)
}

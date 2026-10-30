package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Installed describes a service package on disk.
type Installed struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Origin      string       `json:"origin"`
	SHA256      string       `json:"sha256,omitempty"`
	Installed   time.Time    `json:"installed"`
	Dir         string       `json:"dir"`
	Permissions []Permission `json:"permissions,omitempty"`
}

// InstallPackage verifies and installs a service package under dataDir.
//
// The package is parsed and compiled before anything is written, so a service
// that does not load is refused rather than installed and then failing at first
// use. An existing service of the same id is replaced as a whole, so an upgrade
// cannot leave files of the previous version behind.
//
// Account values are not part of this: a client owns its credentials, and Core
// only keeps the code.
func InstallPackage(ctx context.Context, dataDir, origin, digest string, archive []byte) (*Installed, error) {
	manifest, source, files, err := ReadPackageZip(archive)
	if err != nil {
		return nil, err
	}
	if err := checkCompiles(ctx, manifest, source); err != nil {
		return nil, err
	}
	dir := PackageDir(dataDir, manifest.ID)
	tmp := dir + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	for name, content := range files {
		if err := ValidFilePath(name); err != nil {
			return nil, err
		}
		path := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return nil, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	rec := &Installed{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Origin: origin, SHA256: digest, Installed: time.Now().UTC(), Dir: dir,
		Permissions: append([]Permission(nil), manifest.Permissions...),
	}
	if err := writeInstallRecord(dataDir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// checkCompiles loads the service in a throwaway runtime: a package whose
// manifest is fine but whose JavaScript does not parse is refused at install
// time rather than at the first call.
func checkCompiles(ctx context.Context, manifest *Manifest, source string) error {
	rt := NewRuntime(Host{})
	defer rt.Close()
	return rt.Load(ctx, manifest, source)
}

// PackageDir is where the package of one service lives.
func PackageDir(dataDir, id string) string {
	return filepath.Join(dataDir, "services", id)
}

// InstalledPackages lists what is installed, ordered by id.
func InstalledPackages(dataDir string) ([]Installed, error) {
	records, err := readInstallRecords(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]Installed, 0, len(records))
	for _, rec := range records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// InstalledPackage reads one install record.
func InstalledPackage(dataDir, id string) (*Installed, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(PackageDir(dataDir, id), ManifestFileName)); err != nil {
		return nil, fmt.Errorf("service: %s is not installed: %w", id, err)
	}
	records, err := readInstallRecords(dataDir)
	if err != nil {
		return nil, err
	}
	rec, ok := records[id]
	if !ok {
		// The files are there but the index lost the record: a client can still
		// read the manifest, so say what is known rather than nothing.
		m, _, err := ReadPackage(PackageDir(dataDir, id))
		if err != nil {
			return nil, err
		}
		return &Installed{ID: m.ID, Name: m.Name, Version: m.Version, Dir: PackageDir(dataDir, id),
			Permissions: append([]Permission(nil), m.Permissions...)}, nil
	}
	return &rec, nil
}

// ReadInstalledPackage reads the manifest and entry of an installed service.
func ReadInstalledPackage(dataDir, id string) (*Manifest, string, error) {
	if err := validID(id); err != nil {
		return nil, "", err
	}
	return ReadPackage(PackageDir(dataDir, id))
}

// RemovePackage deletes an installed service.
func RemovePackage(dataDir, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(PackageDir(dataDir, id), ManifestFileName)); err != nil {
		return fmt.Errorf("service: %s is not installed: %w", id, err)
	}
	if err := os.RemoveAll(PackageDir(dataDir, id)); err != nil {
		return err
	}
	records, err := readInstallRecords(dataDir)
	if err != nil {
		return err
	}
	delete(records, id)
	return writeInstallRecords(dataDir, records)
}

// validID keeps an id usable as a directory name.
func validID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return fmt.Errorf("service: %q is not a valid service id", id)
	}
	return nil
}

// indexPath is where the install records live.
func indexPath(dataDir string) string {
	return filepath.Join(dataDir, "services.json")
}

// readInstallRecords reads the index keyed by id.
func readInstallRecords(dataDir string) (map[string]Installed, error) {
	out := map[string]Installed{}
	data, err := os.ReadFile(indexPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("service: the install index %s is corrupt: %w", indexPath(dataDir), err)
	}
	return out, nil
}

// writeInstallRecord adds or replaces one record.
func writeInstallRecord(dataDir string, rec *Installed) error {
	records, err := readInstallRecords(dataDir)
	if err != nil {
		return err
	}
	records[rec.ID] = *rec
	return writeInstallRecords(dataDir, records)
}

// writeInstallRecords writes the whole index through a temporary file.
func writeInstallRecords(dataDir string, records map[string]Installed) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	path := indexPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNotInstalled reports a package that is not on disk.
var ErrNotInstalled = errors.New("service: not installed")

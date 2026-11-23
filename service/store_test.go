package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallPackageWritesAndLists(t *testing.T) {
	data := t.TempDir()
	archive := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle() { return { ok: true }; }",
		"lib/a.mjs":      "export const a = 1;",
	})
	rec, err := InstallPackage(context.Background(), data, "collection:x/y", "abc", archive)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec.ID != "demo" || rec.Version != "1" {
		t.Fatalf("record = %+v", rec)
	}
	for _, name := range []string{ManifestFileName, "index.mjs", filepath.Join("lib", "a.mjs")} {
		if _, err := os.Stat(filepath.Join(rec.Dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	all, err := InstalledPackages(data)
	if err != nil || len(all) != 1 || all[0].ID != "demo" {
		t.Fatalf("list = %+v (%v)", all, err)
	}
	one, err := InstalledPackage(data, "demo")
	if err != nil || one.SHA256 != "abc" || one.Origin != "collection:x/y" {
		t.Fatalf("one = %+v (%v)", one, err)
	}
	if len(one.Permissions) != 1 || one.Permissions[0] != PermissionNetwork {
		t.Fatalf("permissions = %v", one.Permissions)
	}
	m, source, err := ReadInstalledPackage(data, "demo")
	if err != nil || m.ID != "demo" || source == "" {
		t.Fatalf("read = %+v %q (%v)", m, source, err)
	}
}

// A package that does not parse is refused before anything is written.
func TestInstallPackageRefusesBrokenJavaScript(t *testing.T) {
	data := t.TempDir()
	archive := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle( {",
	})
	if _, err := InstallPackage(context.Background(), data, "x", "", archive); err == nil {
		t.Fatal("a package that does not parse was installed")
	}
	if _, err := os.Stat(PackageDir(data, "demo")); err == nil {
		t.Fatal("a refused package left files behind")
	}
}

// Upgrading replaces the whole directory.
func TestInstallPackageReplacesEverything(t *testing.T) {
	data := t.TempDir()
	first := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle() { return 1; }",
		"old.mjs":        "export const old = 1;",
	})
	if _, err := InstallPackage(context.Background(), data, "a", "", first); err != nil {
		t.Fatal(err)
	}
	second := buildZip(t, map[string]string{
		ManifestFileName: zipManifest,
		"index.mjs":      "export function handle() { return 2; }",
	})
	rec, err := InstallPackage(context.Background(), data, "b", "", second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rec.Dir, "old.mjs")); err == nil {
		t.Fatal("a file of the previous version survived")
	}
}

func TestRemovePackage(t *testing.T) {
	data := t.TempDir()
	archive := buildZip(t, map[string]string{ManifestFileName: zipManifest, "index.mjs": "export function handle() {}"})
	if _, err := InstallPackage(context.Background(), data, "a", "", archive); err != nil {
		t.Fatal(err)
	}
	if err := RemovePackage(data, "demo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(PackageDir(data, "demo")); err == nil {
		t.Fatal("the directory survived removal")
	}
	if all, _ := InstalledPackages(data); len(all) != 0 {
		t.Fatalf("list = %+v", all)
	}
	if err := RemovePackage(data, "demo"); err == nil {
		t.Fatal("removing a service that is not installed was accepted")
	}
}

// An id becomes a directory name, so it may not walk out of the data directory.
func TestInstalledPackageRefusesABadID(t *testing.T) {
	data := t.TempDir()
	for _, id := range []string{"", "..", "a/b", `a\b`, ".hidden"} {
		if _, err := InstalledPackage(data, id); err == nil {
			t.Errorf("%q was accepted", id)
		}
		if _, _, err := ReadInstalledPackage(data, id); err == nil {
			t.Errorf("%q was read", id)
		}
	}
}

// A profile whose index was lost still reports what the package says.
func TestInstalledPackageFallsBackToTheManifest(t *testing.T) {
	data := t.TempDir()
	archive := buildZip(t, map[string]string{ManifestFileName: zipManifest, "index.mjs": "export function handle() {}"})
	if _, err := InstallPackage(context.Background(), data, "a", "", archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(indexPath(data)); err != nil {
		t.Fatal(err)
	}
	rec, err := InstalledPackage(data, "demo")
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if rec.ID != "demo" || rec.Version != "1" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestServiceUpgradeMigratesStorage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first := buildZip(t, map[string]string{ManifestFileName: strings.Replace(zipManifest, `"version":"1"`, `"version":"1.0.0"`, 1), "index.mjs": "export function handle() { return 1; }"})
	installed, err := InstallPackage(ctx, dir, "a", "", first)
	if err != nil || installed.Version != "1.0.0" {
		t.Fatalf("install: %+v, %v", installed, err)
	}
	legacy := filepath.Join(PackageDir(dir, "demo"), "storage.json")
	if err := os.WriteFile(legacy, []byte(`{"old":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := buildZip(t, map[string]string{ManifestFileName: strings.Replace(zipManifest, `"version":"1"`, `"version":"2.0.0"`, 1), "index.mjs": "export function handle() { return 2; }"})
	installed, err = InstallPackage(ctx, dir, "b", "", second)
	if err != nil || installed.Version != "2.0.0" {
		t.Fatalf("upgrade: %+v, %v", installed, err)
	}
	store, err := OpenStorage(dir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemovePackage(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.Get(ctx, "old"); err != nil || !found || value != "kept" {
		t.Fatalf("storage after upgrade and removal = %q, %v, %v", value, found, err)
	}
}

func TestServiceStorageResumesMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	packageDir := PackageDir(dir, "demo")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(packageDir, "storage.json")
	if err := os.WriteFile(legacy, []byte(`{"legacy":"kept","shared":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packageDir+".json", []byte(`{"shared":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := OpenStorage(dir, "demo")
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"shared": "new", "legacy": "kept"} {
			if got, found, err := store.Get(ctx, key); err != nil || !found || got != want {
				t.Fatalf("Get(%q) = %q, %v, %v", key, got, found, err)
			}
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy storage remains: %v", err)
	}
}

func TestServiceStorageConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	var writes sync.WaitGroup
	for index := range 20 {
		writes.Go(func() {
			store, err := OpenStorage(dir, "demo")
			if err != nil {
				t.Error(err)
				return
			}
			if err := store.Set(context.Background(), fmt.Sprint(index), "kept"); err != nil {
				t.Error(err)
			}
		})
	}
	writes.Wait()
	values, err := readStorage(PackageDir(dir, "demo") + ".json")
	if err != nil || len(values) != 20 {
		t.Fatalf("storage = %v, %v", values, err)
	}
}

func TestConcurrentServiceInstalls(t *testing.T) {
	dir := t.TempDir()
	var installs sync.WaitGroup
	for index := range 12 {
		id := fmt.Sprintf("demo-%d", index)
		archive := buildZip(t, map[string]string{ManifestFileName: strings.Replace(zipManifest, "demo", id, 1), "index.mjs": "export function handle() {}"})
		installs.Go(func() {
			if _, err := InstallPackage(context.Background(), dir, "", "", archive); err != nil {
				t.Error(err)
			}
		})
	}
	installs.Wait()
	if records, err := InstalledPackages(dir); err != nil || len(records) != 12 {
		t.Fatalf("install records = %v, %v", records, err)
	}
}

func TestServiceInstallRestoresPreviousPackage(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"package", "index"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			first := buildZip(t, map[string]string{ManifestFileName: zipManifest, "index.mjs": "export function handle() { return 1; }"})
			if _, err := InstallPackage(ctx, dir, "first", "", first); err != nil {
				t.Fatal(err)
			}
			second := buildZip(t, map[string]string{ManifestFileName: strings.Replace(zipManifest, `"version":"1"`, `"version":"2.0.0"`, 1), "index.mjs": "export function handle() { return 2; }"})
			injected := errors.New("injected rename error")
			rename := func(from, to string) error {
				if failure == "package" && to == PackageDir(dir, "demo") && strings.HasPrefix(filepath.Base(filepath.Dir(from)), ".install-") ||
					failure == "index" && to == indexPath(dir) {
					return injected
				}
				return os.Rename(from, to)
			}
			if _, err := installPackage(ctx, dir, "", "second", "", second, rename); !errors.Is(err, injected) {
				t.Fatalf("install returned %v", err)
			}
			manifest, source, err := ReadInstalledPackage(dir, "demo")
			if err != nil || manifest.Version != "1" || !strings.Contains(source, "return 1") {
				t.Fatalf("previous package = %+v, %q, %v", manifest, source, err)
			}
			if record, err := InstalledPackage(dir, "demo"); err != nil || record.Origin != "first" {
				t.Fatalf("previous record = %+v, %v", record, err)
			}
		})
	}
}

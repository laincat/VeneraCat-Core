package service

import (
	"context"
	"os"
	"path/filepath"
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

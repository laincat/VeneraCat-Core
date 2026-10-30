package magpieplugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/magpieplugin"
)

// The four community vendor packages that target the desktop's Node/Bun host.
// They are published on npm, so anyone can fetch them and check this again:
//
//	cd /tmp && for p in factory workbuddy qoder grok; do
//	    npm pack @magpie-community/opencode-$p-auth
//	    mkdir $p && tar -xzf magpie-community-opencode-$p-auth-*.tgz -C $p --strip-components=1
//	done
//	MAGPIE_VENDOR_DIR=/tmp go test -run TestVendorPackagesAreRefused -v ./magpieplugin
//
// The test is skipped without the directory: the packages are third-party and not
// vendored here. When it does run, it proves the refusal with the real files rather
// than by reading their imports — the difference between "cannot work" and "the
// entry file mentions fs".
func TestVendorPackagesAreRefused(t *testing.T) {
	root := os.Getenv("MAGPIE_VENDOR_DIR")
	if root == "" {
		t.Skip("MAGPIE_VENDOR_DIR not set: no third-party packages to load")
	}
	dirs, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil || len(dirs) == 0 {
		t.Skipf("MAGPIE_VENDOR_DIR=%s holds no package directories", root)
	}
	wanted := map[string]string{
		"factory":   "node:http",
		"workbuddy": "node:crypto",
		"qoder":     "node:crypto",
		"grok":      "node:child_process",
	}
	seen := 0
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(dir)
		for vendor, module := range wanted {
			if !strings.Contains(name, "-"+vendor+"-auth") {
				continue
			}
			seen++
			_, err := loadPackageDir(dir)
			if err == nil {
				t.Errorf("%s loaded; it was recorded as needing %s", name, module)
				continue
			}
			if !strings.Contains(err.Error(), module) {
				t.Errorf("%s: want a refusal naming %s, got %v", name, module, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "cannot resolve") {
				t.Errorf("%s: refusal should say it cannot resolve the module, got %v", name, err)
			}
		}
	}
	if seen == 0 {
		t.Skipf("MAGPIE_VENDOR_DIR=%s has none of the four vendor packages", root)
	}
}

// loadPackageDir reads a package into a bundle the way a client would, then loads
// it. Kept here so the test above stays about behaviour.
func loadPackageDir(dir string) (*magpieplugin.Loader, error) {
	files := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	bundle, err := magpieplugin.NewBundle(files)
	if err != nil {
		return nil, err
	}
	return magpieplugin.Load(bundle)
}

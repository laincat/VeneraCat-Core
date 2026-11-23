package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	core "github.com/laincat/veneracat-core/gohost"
	"github.com/laincat/veneracat-core/plugin"
)

const firstPersisted = `class First extends ComicSource {
 name = "First"; key = "first_source"; version = "1.0.0"
 search = {load: async () => ({comics: [], maxPage: 1})}
}`

const secondRejected = `class Second extends ComicSource {
 name = "Second"; key = "second_source"; version = "1.0.0"
 search = {load: async () => ({comics: [], maxPage: 1})}
}`

// An install Core accepts but the disk refuses must not survive in memory. The
// window would show a source that the next launch loses without a word, and the
// user has already been told the install failed. The read-only manifest is what
// a full disk or a permission problem looks like to this code.
func TestAFailedPersistLeavesNoInstalledSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	c, err := core.Open(core.Options{DataDir: root, AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lib := New(c)

	if _, err := lib.InstallTypedText(ctx, plugin.TypeComic, firstPersisted, ""); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "plugins.json")
	if err := os.Chmod(manifest, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o600) })

	if _, err := lib.InstallTypedText(ctx, plugin.TypeComic, secondRejected, ""); err == nil {
		t.Skip("this platform still allowed the manifest write; the failure path was not reached")
	}
	if lib.IsInstalled("second_source") {
		t.Fatal("the rejected source is still installed in Core")
	}
	if !lib.IsInstalled("first_source") {
		t.Fatal("the rollback removed the source that had already been saved")
	}
}

package cookies

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedImportLeavesMemoryAndDiskUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	saved := &http.Cookie{Name: "saved", Value: "value", Domain: "example.test", Expires: time.Now().Add(time.Hour)}
	if err := jar.Import("comic:example", []*http.Cookie{saved}); err == nil {
		t.Fatal("import hid the write failure")
	}
	if got := jar.Header("comic:example", "https://example.test"); got != "" {
		t.Fatalf("failed import published %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := jar.Import("comic:example", []*http.Cookie{saved}); err != nil {
		t.Fatal(err)
	}
	if got := jar.Header("comic:example", "https://example.test"); got != "saved=value" {
		t.Fatalf("retry did not publish: %q", got)
	}
}

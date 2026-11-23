package cookies

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditNullPersistedJarRemainsWritable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(p, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	jar, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetURL("p", mustURL(t, "https://example.test"), []*http.Cookie{{Name: "sid", Value: "one"}})
	if got := jar.Header("p", "https://example.test"); got != "sid=one" {
		t.Fatalf("header=%q", got)
	}
}

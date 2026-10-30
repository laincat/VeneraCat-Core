package kazumi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownUnparsable lists published rules that Parse legitimately rejects,
// with the reason.
var knownUnparsable = map[string]string{
	"XY": "deprecated rule with an empty searchResult; Kazumi itself fails every search with it",
}

func TestParseAllPublishedRules(t *testing.T) {
	files, err := filepath.Glob("testdata/rules/*.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed := 0
	keys := map[string]string{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Parse(data)
		if reason, skip := knownUnparsable[name]; skip {
			if err == nil {
				t.Errorf("%s: listed as unparsable (%s) but parses", name, reason)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		parsed++
		if k := r.Key(); !strings.HasPrefix(k, "kz_") || strings.ContainsAny(k, " /.-") {
			t.Errorf("%s: bad key %q", name, k)
		} else if prev, dup := keys[k]; dup {
			t.Errorf("%s: key %q already used by %s", name, k, prev)
		} else {
			keys[k] = name
		}
	}
	if len(files) < 80 {
		t.Fatalf("only %d rule fixtures found", len(files))
	}
	t.Logf("%d of %d published rules parse", parsed, len(files))
}

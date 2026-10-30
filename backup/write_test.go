package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// writeThenRead is the round trip that matters: Core could read this format and
// never write it, so a user could move in and never out.
func writeThenRead(t *testing.T, opts WriteOptions) *Content {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, opts); err != nil {
		t.Fatalf("write: %v", err)
	}
	archive, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	content, err := archive.Read(context.Background(), Options{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return content
}

func TestWriteThenReadKeepsSources(t *testing.T) {
	content := writeThenRead(t, WriteOptions{
		Sources: []WriteSource{
			{Name: "b.js", Body: []byte(`class B extends ComicSource { name='B' }`)},
			{Name: "a.js", Body: []byte(`class A extends ComicSource { name='A' }`)},
		},
	})
	if len(content.Sources) != 2 {
		t.Fatalf("sources = %+v", content.Sources)
	}
	// The reader sorts by name, and so does the writer.
	if content.Sources[0].Key != "a" || content.Sources[1].Key != "b" {
		t.Errorf("order = %v, %v", content.Sources[0].Key, content.Sources[1].Key)
	}
	if !strings.Contains(content.Sources[0].Script, "extends ComicSource") {
		t.Errorf("body = %q", content.Sources[0].Script)
	}
}

func TestWriteThenReadKeepsSettingsAndHistory(t *testing.T) {
	content := writeThenRead(t, WriteOptions{
		Settings: map[string]json.RawMessage{
			"language":     json.RawMessage(`"zh_CN"`),
			"dailyLimit":   json.RawMessage(`30`),
			"showNSFW":     json.RawMessage(`true`),
			"deviceId":     json.RawMessage(`"drop-me"`),
			"proxy":        json.RawMessage(`"http://drop"`),
			"authorizatio": json.RawMessage(`null`),
		},
		SearchHistory: []string{"one piece", "进击的巨人"},
	})
	if got := string(content.Settings["language"]); got != `"zh_CN"` {
		t.Errorf("language = %s", got)
	}
	if got := string(content.Settings["dailyLimit"]); got != "30" {
		t.Errorf("dailyLimit = %s", got)
	}
	if got := string(content.Settings["showNSFW"]); got != "true" {
		t.Errorf("showNSFW = %s", got)
	}
	// Device settings are dropped on the way out, as the reader drops them on
	// the way in: they describe one machine, not the account.
	for _, key := range []string{"deviceId", "proxy"} {
		if _, ok := content.Settings[key]; ok {
			t.Errorf("%s should not travel", key)
		}
	}
	if len(content.SearchHistory) != 2 || content.SearchHistory[1] != "进击的巨人" {
		t.Errorf("history = %v", content.SearchHistory)
	}
}

// Two exports of the same data are byte-identical, so "did anything change" has
// an answer.
func TestWriteIsDeterministic(t *testing.T) {
	opts := WriteOptions{
		Sources: []WriteSource{
			{Name: "z.js", Body: []byte("z")},
			{Name: "a.js", Body: []byte("a")},
		},
		Settings: map[string]json.RawMessage{"b": json.RawMessage(`2`), "a": json.RawMessage(`1`)},
	}
	var first, second bytes.Buffer
	if err := Write(&first, opts); err != nil {
		t.Fatal(err)
	}
	if err := Write(&second, opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("two writes of the same data differ")
	}
}

// A name that would escape the archive, or an empty script, is refused rather
// than exported as something the reader will choke on.
func TestWriteRefusesBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts WriteOptions
	}{
		{"nothing", WriteOptions{}},
		{"parent", WriteOptions{Sources: []WriteSource{{Name: "../evil.js", Body: []byte("x")}}}},
		{"path", WriteOptions{Sources: []WriteSource{{Name: "a/b.js", Body: []byte("x")}}}},
		{"backslash", WriteOptions{Sources: []WriteSource{{Name: `a\b.js`, Body: []byte("x")}}}},
		{"empty body", WriteOptions{Sources: []WriteSource{{Name: "a.js"}}}},
		{"duplicate", WriteOptions{Sources: []WriteSource{{Name: "a.js", Body: []byte("x")}, {Name: "a.js", Body: []byte("y")}}}},
	} {
		var buf bytes.Buffer
		if err := Write(&buf, tc.opts); err == nil {
			t.Errorf("%s: write succeeded", tc.name)
		}
	}
}

// The cookies this client holds are per-source credentials, and an export is a
// file people mail to themselves: none of it is written.
func TestWriteLeavesCookiesOut(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, WriteOptions{
		Sources:  []WriteSource{{Name: "a.js", Body: []byte("x")}},
		Settings: map[string]json.RawMessage{"k": json.RawMessage(`1`)},
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if archive.Has(MemberCookies) {
		t.Error("the export contains cookies")
	}
}

// A restore brings back code; the source's own stored values have to come with
// it, or it wakes up logged out.
func TestWriteCarriesSourceData(t *testing.T) {
	var buf bytes.Buffer
	err := Write(&buf, WriteOptions{
		Sources: []WriteSource{{
			Name: "cat.js",
			Body: []byte("class Cat {}"),
			Data: map[string]json.RawMessage{
				"token":   json.RawMessage(`"abc"`),
				"account": json.RawMessage(`["u","p"]`),
			},
		}},
		Settings:      map[string]json.RawMessage{},
		SearchHistory: nil,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	arch, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	content, err := arch.Read(context.Background(), Options{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(content.Sources) != 1 {
		t.Fatalf("sources = %+v", content.Sources)
	}
	src := content.Sources[0]
	if !src.HasData {
		t.Fatal("the data did not travel")
	}
	if !strings.Contains(string(src.Data), `"token":"abc"`) {
		t.Errorf("data = %s", src.Data)
	}
	if !src.HasScript || src.Script != "class Cat {}" {
		t.Errorf("script = %q", src.Script)
	}
}

// The reader caps every comic_source file; a writer with no cap of its own would
// produce an archive this package cannot open.
func TestWriteRefusesDataItCouldNotReadBack(t *testing.T) {
	huge := map[string]json.RawMessage{"blob": json.RawMessage(`"` + strings.Repeat("x", MaxWriteDataBytes) + `"`)}
	var buf bytes.Buffer
	err := Write(&buf, WriteOptions{
		Sources:       []WriteSource{{Name: "cat.js", Body: []byte("class Cat {}"), Data: huge}},
		Settings:      map[string]json.RawMessage{},
		SearchHistory: nil,
	})
	if err == nil {
		t.Fatal("a source's data larger than the reader allows was written anyway")
	}
	if !strings.Contains(err.Error(), "cat.js") {
		t.Errorf("the error does not name the source: %v", err)
	}

	// A data object well under the cap comes back readable.
	ok := map[string]json.RawMessage{"blob": json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)}
	buf.Reset()
	if err := Write(&buf, WriteOptions{
		Sources:       []WriteSource{{Name: "cat.js", Body: []byte("class Cat {}"), Data: ok}},
		Settings:      map[string]json.RawMessage{},
		SearchHistory: nil,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	arch, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatalf("the writer produced an archive the reader refuses: %v", err)
	}
	if content, err := arch.Read(context.Background(), Options{}); err != nil || len(content.Sources) != 1 {
		t.Fatalf("read: %v %d", err, len(content.Sources))
	}
}

// Why the writer needs a cap of its own: the reader refuses a comic_source
// member above its default, so the cap is the boundary, not a preference.
func TestReaderRejectsDataAboveItsCap(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(SourceDir + "cat.data")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"blob":"` + strings.Repeat("x", MaxWriteDataBytes) + `"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{}); err == nil {
		t.Fatal("the reader accepted a member it is supposed to cap")
	}
}

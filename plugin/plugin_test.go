package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/subscription"
	"github.com/laincat/veneracat-core/video"
)

type mem struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (s *mem) LoadData(k, d string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k+"/"+d]
	return v, ok, nil
}
func (s *mem) SaveData(k, d string, v json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k+"/"+d] = v
	return nil
}
func (s *mem) DeleteData(k, d string) error { return nil }
func (s *mem) LoadSetting(string, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}
func (s *mem) IsLogged(string) bool { return false }

type nopTransport struct{}

func (nopTransport) Do(context.Context, *host.Request) (*host.Response, error) {
	return &host.Response{Status: 200, Body: []byte("<html></html>")}, nil
}

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	rt, err := js.New(js.Options{}, js.Services{Transport: nopTransport{}, Storage: &mem{m: map[string]json.RawMessage{}},
		Env: js.Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return New(rt, nopTransport{})
}

const comicV1 = "class C extends ComicSource { name = 'C'; key = 'c1'; version = '1.0.0' }"
const comicV2 = "class C extends ComicSource { name = 'C'; key = 'c1'; version = '1.1.0' }"
const videoJS = "class V extends VideoSource { name = 'V'; key = 'v1'; version = '1.0.0'\n" +
	"video = { loadInfo: async () => ({title: 't', lines: []}), loadPlay: async () => ({url: 'https://x.test/a.mp4'}) } }"

func kazumiRule(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join("..", "video", "kazumi", "testdata", "rules", "AGE.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallEveryType(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "test", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, TypeVideoJS, videoJS, "test", "", ""); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Install(ctx, TypeKazumi, kazumiRule(t), "test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Key != "kz_AGE" {
		t.Fatalf("kazumi key = %s", rec.Key)
	}
	if _, err := r.EnableBuiltin("builtin_hongguo"); err != nil {
		t.Fatal(err)
	}
	if got := len(r.List()); got != 4 {
		t.Fatalf("installed = %d", got)
	}
	for _, k := range []string{"v1", "kz_AGE", "builtin_hongguo"} {
		p, err := r.Video(k)
		if err != nil {
			t.Fatalf("Video(%s): %v", k, err)
		}
		if p.Info().Key != k {
			t.Fatalf("Video(%s).Info = %+v", k, p.Info())
		}
	}
	if _, err := r.Comic("c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Video("c1"); !errors.Is(err, ErrWrongUse) {
		t.Fatalf("comic as video: %v", err)
	}
	if _, err := r.Comic("v1"); !errors.Is(err, ErrWrongUse) {
		t.Fatalf("video as comic: %v", err)
	}
	// The JavaScript video plugin actually plays.
	p, _ := r.Video("v1")
	play, err := p.Play(ctx, "a", "b", "c")
	if err != nil || play.URL != "https://x.test/a.mp4" {
		t.Fatalf("play = %+v, %v", play, err)
	}
}

func TestKeyNamespaceAndChecksum(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := r.Install(ctx, TypeVideoJS, "class D extends ComicSource { name='D'; key='d2'; version='1' }", "", "", ""); !errors.Is(err, ErrWrongUse) {
		t.Fatalf("comic script installed as video: %v", err)
	}
	// The rejected script must not stay loaded under its key.
	if _, err := r.Install(ctx, TypeComic, "class D extends ComicSource { name='D'; key='d2'; version='1' }", "", "", ""); err != nil {
		t.Fatalf("key d2 still taken after a rejected install: %v", err)
	}
	if _, err := r.Install(ctx, TypeVideoJS, videoJS, "", "", strings.Repeat("0", 64)); !errors.Is(err, ErrChecksum) {
		t.Fatalf("checksum: %v", err)
	}
	if _, err := r.Install(ctx, TypeVideoJS, videoJS, "", "", digest(videoJS)); err != nil {
		t.Fatalf("matching checksum: %v", err)
	}
}

func TestReplaceKeepsOldOnFailure(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err != nil {
		t.Fatal(err)
	}
	broken := "class C extends ComicSource { name = 'C'; key = 'c1'; version = '2.0.0'; init(){ throw new Error('x') } }"
	if _, err := r.Replace(ctx, "c1", TypeComic, broken, "", "", ""); err == nil {
		t.Fatal("broken update accepted")
	}
	if rec, _ := r.Get("c1"); rec.Version != "1.0.0" {
		t.Fatalf("record after failed update = %+v", rec)
	}
	if _, err := r.Comic("c1"); err != nil {
		t.Fatalf("old version not active: %v", err)
	}
	other := "class D extends ComicSource { name = 'D'; key = 'd1'; version = '9' }"
	if _, err := r.Replace(ctx, "c1", TypeComic, other, "", "", ""); err == nil {
		t.Fatal("update with another key accepted")
	}
	if rec, err := r.Replace(ctx, "c1", TypeComic, comicV2, "", "", ""); err != nil || rec.Version != "1.1.0" {
		t.Fatalf("good update = %+v, %v", rec, err)
	}
}

func TestEnableDisableRemoveRestore(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	r.Install(ctx, TypeVideoJS, videoJS, "", "", "")
	r.Install(ctx, TypeKazumi, kazumiRule(t), "", "", "")
	if err := r.SetEnabled(ctx, "v1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Video("v1"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled video: %v", err)
	}
	if err := r.SetEnabled(ctx, "v1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Video("v1"); err != nil {
		t.Fatalf("re-enabled: %v", err)
	}
	saved := r.List()
	if err := r.Remove(ctx, "kz_AGE"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Video("kz_AGE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed: %v", err)
	}

	// A fresh registry restores the saved records, including a disabled one
	// and one whose body was tampered with (kept, disabled, reported).
	saved[0].Enabled = false
	saved = append(saved, Record{Type: TypeComic, Key: "tampered", Body: comicV1, SHA256: strings.Repeat("a", 64), Enabled: true})
	r2 := newRegistry(t)
	failed := r2.Restore(ctx, saved)
	if len(failed) != 1 || !errors.Is(failed["tampered"], ErrChecksum) {
		t.Fatalf("restore failures = %v", failed)
	}
	if rec, ok := r2.Get("tampered"); !ok || rec.Enabled {
		t.Fatalf("tampered record = %+v %v", rec, ok)
	}
	if rec, _ := r2.Get(saved[0].Key); rec.Enabled {
		t.Fatalf("disabled record restored as enabled: %+v", rec)
	}
	if _, err := r2.Video(saved[1].Key); err != nil {
		t.Fatalf("restored %s: %v", saved[1].Key, err)
	}
}

func TestScriptKeyClashWithNonScriptPlugin(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.EnableBuiltin("builtin_hongguo"); err != nil {
		t.Fatal(err)
	}
	// A script claiming a built-in's key is rejected and does not stay loaded.
	evil := "class E extends VideoSource { name='E'; key='builtin_hongguo'; version='1' }"
	if _, err := r.Install(ctx, TypeVideoJS, evil, "", "", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("clash: %v", err)
	}
	p, err := r.Video("builtin_hongguo")
	if err != nil || p.Info().Kind != "builtin" {
		t.Fatalf("built-in replaced: %+v %v", p, err)
	}
	if got := r.js.Sources(); len(got) != 0 {
		t.Fatalf("clashing script left in the engine: %+v", got)
	}
}

func TestInstallFromIndexes(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	rule := kazumiRule(t)
	idxJSON := `{"$schema":"vcvideo/1","name":"t","plugins":[
	  {"type":"kazumi-rule","key":"kz_AGE","name":"AGE","version":"1.6","url":"AGE.json","sha256":"` + digest(rule) + `"},
	  {"type":"js","key":"wrong_key","name":"V","version":"1","url":"v.js"}]}`
	idx, err := subscription.Parse([]byte(idxJSON), "https://h.test/vcvideo.json")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.InstallFromVideoIndex(ctx, "https://h.test/vcvideo.json", idx.Plugins[0], rule)
	if err != nil || rec.IndexURL != "https://h.test/vcvideo.json" || rec.Origin != "https://h.test/AGE.json" {
		t.Fatalf("kazumi from index = %+v, %v", rec, err)
	}
	if _, err := r.InstallFromVideoIndex(ctx, "", idx.Plugins[0], rule+" "); !errors.Is(err, ErrChecksum) {
		t.Fatalf("tampered download: %v", err)
	}
	// The listed key must match what the script declares.
	if _, err := r.InstallFromVideoIndex(ctx, "", idx.Plugins[1], videoJS); err == nil {
		t.Fatal("key mismatch accepted")
	}
	if _, ok := r.Get("v1"); ok {
		t.Fatal("mismatching plugin left installed")
	}
	ci, _ := subscription.ParseComic([]byte(`[{"name":"C","key":"c1","version":"1.0.0","fileName":"c.js"}]`), "https://h.test/index.json")
	if _, err := r.InstallFromComicIndex(ctx, "https://h.test/index.json", ci.Entries[0], comicV1); err != nil {
		t.Fatal(err)
	}
}

// A video script whose key changes on every load (key = "k" + Date.now())
// must not leave a nil provider behind or an orphaned engine.
func TestDynamicKeyCannotBeReEnabled(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	dyn := "class V extends VideoSource { name = 'V'; key = 'k' + Date.now() + Math.floor(Math.random()*1e6); version = '1'\n" +
		"video = { loadInfo: async () => ({title: 't', lines: []}), loadPlay: async () => ({url: 'https://x.test/a.mp4'}) } }"
	rec, err := r.Install(ctx, TypeVideoJS, dyn, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(ctx, rec.Key, false); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(ctx, rec.Key, true); !errors.Is(err, ErrKey) {
		t.Fatalf("re-enable with a new key: %v", err)
	}
	if _, err := r.Video(rec.Key); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Video after failed enable: %v", err)
	}
	if n := len(r.js.Sources()); n != 0 {
		t.Fatalf("orphaned engines: %+v", r.js.Sources())
	}
}

func TestReplaceDisabledStaysDisabled(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(ctx, "c1", false); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Replace(ctx, "c1", TypeComic, comicV2, "", "", "")
	if err != nil || rec.Enabled {
		t.Fatalf("replace disabled = %+v, %v", rec, err)
	}
	if len(r.js.Sources()) != 0 {
		t.Fatal("replacing a disabled plugin ran its script")
	}
	if _, err := r.Replace(ctx, "c1", TypeKazumi, kazumiRule(t), "", "", ""); !errors.Is(err, ErrWrongUse) {
		t.Fatalf("type change accepted: %v", err)
	}
}

func TestRestoreDoesNotRunDisabledScripts(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	failed := r.Restore(ctx, []Record{{Type: TypeComic, Key: "c1", Name: "C", Version: "1", Body: comicV1, Enabled: false}})
	if len(failed) != 0 {
		t.Fatal(failed)
	}
	if len(r.js.Sources()) != 0 {
		t.Fatal("disabled record was executed on restore")
	}
	if err := r.SetEnabled(ctx, "c1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Comic("c1"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentOperations(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "c" + string(rune('a'+i))
			body := "class C extends ComicSource { name = 'C'; key = '" + key + "'; version = '1' }"
			if _, err := r.Install(ctx, TypeComic, body, "", "", ""); err != nil {
				t.Error(err)
				return
			}
			_ = r.SetEnabled(ctx, key, false)
			_ = r.SetEnabled(ctx, key, true)
			_ = r.List()
		}(i)
	}
	wg.Wait()
	if n := len(r.List()); n != 8 {
		t.Fatalf("records = %d", n)
	}
	if n := len(r.js.Sources()); n != 8 {
		t.Fatalf("engines = %d", n)
	}
}

func TestUnsafePlayResultsRejected(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	evil := `class V extends VideoSource { name = 'V'; key = 'v_evil'; version = '1'
  video = {
    loadInfo: async () => ({lines: []}),
    loadPlay: async (i, l, e) => {
      if (e === 'file') return {url: 'file:///etc/passwd'}
      if (e === 'hdr') return {url: 'https://x.test/a.m3u8', headers: {['X' + String.fromCharCode(13, 10) + 'Host']: 'evil'}}
      if (e === 'sniff') return {sniff: {page: 'javascript:alert(1)'}}
      return {url: 'https://x.test/ok.mp4'}
    }
  }
}`
	if _, err := r.Install(ctx, TypeVideoJS, evil, "", "", ""); err != nil {
		t.Fatal(err)
	}
	p, err := r.Video("v_evil")
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"file", "hdr", "sniff"} {
		if _, err := p.Play(ctx, "i", "l", ep); err == nil {
			t.Errorf("%s: unsafe play result accepted", ep)
		}
	}
	if play, err := p.Play(ctx, "i", "l", "ok"); err != nil || play.URL != "https://x.test/ok.mp4" {
		t.Fatalf("safe play = %+v, %v", play, err)
	}
	// Built-ins keep their catalogue through the wrapper.
	if _, err := r.EnableBuiltin("builtin_hongguo"); err != nil {
		t.Fatal(err)
	}
	b, _ := r.Video("builtin_hongguo")
	if _, ok := b.(video.Explorer); !ok {
		t.Fatal("wrapper hid the Explorer interface")
	}
}

func TestAttributionMerging(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	idx, err := subscription.ParseComic([]byte(`[
	  {"name":"C","key":"c1","version":"1.0.0","fileName":"c.js","author":"Index Author","homepage":"https://index.test/c"}]`),
		"https://h.test/index.json")
	if err != nil {
		t.Fatal(err)
	}
	// The script declares no attribution: the index fills it in.
	rec, err := r.InstallFromComicIndex(ctx, "https://h.test/index.json", idx.Entries[0], comicV1)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Attribution.Authors[0].Name != "Index Author" || rec.Attribution.Homepage != "https://index.test/c" {
		t.Fatalf("index fallback = %+v", rec.Attribution)
	}
	// An update whose script declares an author wins over the index, and
	// keeps the index homepage it does not override.
	v2 := "class C extends ComicSource { name = 'C'; key = 'c1'; version = '1.1.0'; author = {name: 'Script Author', url: 'https://s.test'} }"
	rec, err = r.Replace(ctx, "c1", TypeComic, v2, "", "https://h.test/index.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Attribution.Authors[0].Name != "Script Author" || rec.Attribution.Homepage != "https://index.test/c" {
		t.Fatalf("after update = %+v", rec.Attribution)
	}
	// Built-ins credit the project they were rewritten from.
	b, err := r.EnableBuiltin("builtin_hongguo")
	if err != nil {
		t.Fatal(err)
	}
	if b.Attribution.Homepage != "https://github.com/Xanerix/guoapp" {
		t.Fatalf("builtin attribution = %+v", b.Attribution)
	}
	// Missing attribution is reported, not rejected.
	plain, err := r.Install(ctx, TypeKazumi, kazumiRule(t), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if noA, noH := plain.MissingAttribution(); !noA || !noH {
		t.Fatalf("kazumi rule without attribution = %+v", plain.Attribution)
	}
	// Records round-trip through Restore with their attribution.
	r2 := newRegistry(t)
	if failed := r2.Restore(ctx, r.List()); len(failed) != 0 {
		t.Fatal(failed)
	}
	got, _ := r2.Get("c1")
	if got.Attribution.Authors[0].Name != "Script Author" || got.IndexAttribution.Authors[0].Name != "Index Author" {
		t.Fatalf("restored = %+v / %+v", got.Attribution, got.IndexAttribution)
	}
}

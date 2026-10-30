package js

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStorage struct {
	mu     sync.Mutex
	data   map[string]json.RawMessage
	sets   map[string]json.RawMessage
	logged bool
}

func newMem() *memStorage {
	return &memStorage{data: map[string]json.RawMessage{}, sets: map[string]json.RawMessage{}}
}
func (m *memStorage) LoadData(k, d string) (json.RawMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[k+"/"+d]
	return v, ok, nil
}
func (m *memStorage) SaveData(k, d string, v json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[k+"/"+d] = append(json.RawMessage(nil), v...)
	return nil
}
func (m *memStorage) DeleteData(k, d string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, k+"/"+d)
	return nil
}
func (m *memStorage) LoadSetting(k, s string) (json.RawMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.sets[k+"/"+s]
	return v, ok, nil
}
func (m *memStorage) IsLogged(string) bool { return m.logged }

func newRT(t *testing.T, opts Options, st *memStorage) *Runtime {
	t.Helper()
	if st == nil {
		st = newMem()
	}
	r, err := New(opts, Services{Storage: st, Env: Environment{Locale: "zh_CN", Platform: "ios", AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func eval(t *testing.T, r *Runtime, src string) string {
	t.Helper()
	out, err := r.Eval(context.Background(), src)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return string(out)
}

func TestInitGlobals(t *testing.T) {
	r := newRT(t, Options{}, nil)
	got := eval(t, r, `[typeof Convert, typeof HtmlDocument, typeof ComicSource, typeof Image, typeof setTimeout, typeof sendMessage, appVersion]`)
	if got != `["object","function","function","function","function","function","1.6.0"]` {
		t.Fatal(got)
	}
}

func TestConvertThroughJS(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if got := eval(t, r, `Convert.hexEncode(Convert.md5(Convert.encodeUtf8("abc")))`); got != `"900150983cd24fb0d6963f7d28e17f72"` {
		t.Fatalf("md5 = %s", got)
	}
	if got := eval(t, r, `Convert.encodeBase64(Convert.encodeUtf8("hi"))`); got != `"aGk="` {
		t.Fatalf("base64 = %s", got)
	}
	if got := eval(t, r, `Convert.decodeUtf8(Convert.decodeBase64("5Lit5paH"))`); got != `"中文"` {
		t.Fatalf("utf8 = %s", got)
	}
	// A failing conversion yields null, as the official host does.
	if got := eval(t, r, `Convert.encryptAesEcb(Convert.encodeUtf8("short"), Convert.encodeUtf8("k"))`); got != "null" {
		t.Fatalf("bad aes = %s", got)
	}
}

func TestHTMLThroughJS(t *testing.T) {
	r := newRT(t, Options{}, nil)
	got := eval(t, r, `(function(){
		var d = new HtmlDocument('<ul><li class="a">one</li><li class="a">two</li></ul>');
		var all = d.querySelectorAll('li.a').map(function(e){ return e.text; });
		var first = d.querySelector('ul').children[1].text;
		d.dispose();
		return [all, first];
	})()`)
	if got != `[["one","two"],"two"]` {
		t.Fatal(got)
	}
}

func TestImageThroughJS(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if got := eval(t, r, `var im = Image.empty(4, 3); [im.width, im.height, im.copyAndRotate90().width]`); got != `[4,3,3]` {
		t.Fatal(got)
	}
}

func TestStorageRules(t *testing.T) {
	st := newMem()
	r := newRT(t, Options{}, st)
	ctx := context.Background()
	if _, err := r.LoadSource(ctx, `class S extends ComicSource { name = "S"; key = "s1"; version = "1"; settings = {mode: {default: "grid"}} }`); err != nil {
		t.Fatal(err)
	}
	in := func(src string) string {
		t.Helper()
		out, err := r.evalIn(ctx, "s1", src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return string(out)
	}
	// Big integers survive a save/load round trip exactly.
	in(`ComicSource.sources.s1.saveData("big", {id: 12345678901234567890n}); null`)
	if got := string(st.data["s1/big"]); got != `{"id":12345678901234567890}` {
		t.Fatalf("stored = %s", got)
	}
	if got := in(`String(ComicSource.sources.s1.loadData("big").id)`); !strings.Contains(got, "1234567890123456") {
		t.Fatalf("loaded = %s", got)
	}
	if _, err := r.evalIn(ctx, "s1", `ComicSource.sources.s1.saveData("setting", 1)`); err == nil || !strings.Contains(err.Error(), "setting is not allowed") {
		t.Fatalf("setting save: %v", err)
	}
	if got := in(`ComicSource.sources.s1.loadSetting("mode")`); got != `"grid"` {
		t.Fatalf("default setting = %s", got)
	}
	st.sets["s1/mode"] = json.RawMessage(`"list"`)
	if got := in(`ComicSource.sources.s1.loadSetting("mode")`); got != `"list"` {
		t.Fatalf("stored setting = %s", got)
	}
	if _, err := r.evalIn(ctx, "s1", `ComicSource.sources.s1.loadSetting("nope")`); err == nil || !strings.Contains(err.Error(), "Setting not found: nope") {
		t.Fatalf("missing setting: %v", err)
	}
	in(`ComicSource.sources.s1.deleteData("big"); null`)
	if got := in(`ComicSource.sources.s1.loadData("big")`); got != "null" {
		t.Fatalf("after delete = %s", got)
	}
	st.logged = true
	if got := in(`ComicSource.sources.s1.isLogged`); got != "true" {
		t.Fatalf("isLogged = %s", got)
	}
	// The global engine has no source storage.
	if _, err := r.Eval(ctx, `sendMessage({method: "load_data", key: "s1", data_key: "x"})`); err == nil {
		t.Fatal("global engine reached source storage")
	}
}

func TestUnknownMethodThrows(t *testing.T) {
	r := newRT(t, Options{}, nil)
	got := eval(t, r, `try { sendMessage({method: "nope"}); "no" } catch (e) { e.message }`)
	if got != `"No JavaScript message handler provided for method: nope"` {
		t.Fatal(got)
	}
}

func TestHostPanicKeepsRuntimeUsable(t *testing.T) {
	r := newRT(t, Options{}, nil)
	testHook = func(m string) {
		if m == "uuid" {
			panic("boom")
		}
	}
	defer func() { testHook = nil }()
	got := eval(t, r, `try { createUuid(); "no" } catch (e) { "caught" }`)
	if got != `"caught"` {
		t.Fatal(got)
	}
	testHook = nil
	if got := eval(t, r, `createUuid().length`); got != "36" {
		t.Fatal(got)
	}
}

func TestTimeoutInterruptsAndRecovers(t *testing.T) {
	r := newRT(t, Options{CallTimeout: 200 * time.Millisecond}, nil)
	start := time.Now()
	_, err := r.Eval(context.Background(), `while (true) {}`)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("interrupt too slow")
	}
	if got := eval(t, r, `1 + 1`); got != "2" {
		t.Fatal(got)
	}
}

func TestMemoryLimit(t *testing.T) {
	r := newRT(t, Options{MemoryLimit: 8 << 20}, nil)
	_, err := r.Eval(context.Background(), `var a = []; while (true) a.push("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + a.length);`)
	if err == nil {
		t.Fatal("runaway allocation was not stopped")
	}
	if got := eval(t, r, `"ok"`); got != `"ok"` {
		t.Fatal(got)
	}
}

func TestAwaitPromise(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if got := eval(t, r, `(async function(){ return {n: 1 + 2}; })()`); got != `{"n":3}` {
		t.Fatal(got)
	}
	_, err := r.Eval(context.Background(), `(async function(){ throw new Error("bad") })()`)
	var se *ScriptError
	if !errors.As(err, &se) || se.Message != "bad" {
		t.Fatalf("rejection: %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Close()
	if _, err := r.Eval(context.Background(), `1`); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestConcurrentEval(t *testing.T) {
	r := newRT(t, Options{}, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				out, err := r.Eval(context.Background(), `Convert.encodeBase64(Convert.encodeUtf8("x"))`)
				if err != nil || string(out) != `"eA=="` {
					errs <- errors.New(string(out))
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

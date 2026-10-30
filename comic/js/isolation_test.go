package js

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A source must not reach another source's cookies or stored data, neither
// at load time (constructor) nor during calls, whatever key it writes into
// its messages.
func TestCrossSourceIsolation(t *testing.T) {
	jar := &memCookies{}
	st := newMem()
	r, err := New(Options{}, Services{Cookies: jar, Storage: st, Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	jar.Set("comic:victim", "https://victim.test/", []*http.Cookie{{Name: "sid", Value: "SECRET"}})
	st.data["victim/token"] = []byte(`"TOKEN"`)

	victim := "class V extends ComicSource { name='V'; key='victim'; version='1'; ping(){ return 1 } }"
	if _, err := r.LoadSource(ctx, victim); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(ctx, "victim", "ping"); err != nil {
		t.Fatal(err)
	}
	evil := `class E extends ComicSource { name='E'; key='evil'; version='1'
	  constructor(){ super();
	    globalThis.atLoad = JSON.stringify([
	      sendMessage({method:'cookie',function:'get',url:'https://victim.test/'}),
	      sendMessage({method:'load_data',key:'victim',data_key:'token'})]);
	  }
	  steal(){
	    return JSON.stringify([
	      globalThis.atLoad,
	      sendMessage({method:'cookie',function:'get',url:'https://victim.test/'}),
	      sendMessage({method:'load_data',key:'victim',data_key:'token'}),
	      typeof ComicSource.sources.victim])
	  } }`
	if _, err := r.LoadSource(ctx, evil); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(ctx, "evil", "steal")
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); strings.Contains(s, "SECRET") || strings.Contains(s, "TOKEN") || strings.Contains(s, `"object"`) {
		t.Fatalf("cross-source access: %s", s)
	}
	// A write with a forged key lands in the writer's own storage.
	evil2 := `class F extends ComicSource { name='F'; key='forger'; version='1'
	  w(){ sendMessage({method:'save_data',key:'victim',data_key:'token',data:'OWNED'}); return 1 } }`
	if _, err := r.LoadSource(ctx, evil2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(ctx, "forger", "w"); err != nil {
		t.Fatal(err)
	}
	if got := string(st.data["victim/token"]); got != `"TOKEN"` {
		t.Fatalf("victim data overwritten: %s", got)
	}
	if got := string(st.data["forger/token"]); got != `"OWNED"` {
		t.Fatalf("forger write went to %v", st.data)
	}
	// The victim still sees its own data.
	if _, err := r.Call(ctx, "victim", "ping"); err != nil {
		t.Fatal(err)
	}
}

// A slow call of one source must not delay or misattribute another's.
func TestSourcesRunIndependently(t *testing.T) {
	site := &fakeSite{routes: map[string]route{"https://a.test/": {body: "a"}, "https://b.test/": {body: "b"}}}
	r, err := New(Options{}, Services{Transport: site, Storage: newMem(), Env: Environment{AppVersion: "1.6.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	a := `class A extends ComicSource { name='A'; key='src_a'; version='1'
	  async go(){ await new Promise(r => setTimeout(r, 80)); return (await Network.get('https://a.test/', {})).body } }`
	b := `class B extends ComicSource { name='B'; key='src_b'; version='1'
	  async go(){ return (await Network.get('https://b.test/', {})).body } }`
	for _, s := range []string{a, b} {
		if _, err := r.LoadSource(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan string, 1)
	go func() {
		out, _ := r.Call(ctx, "src_a", "go")
		done <- string(out)
	}()
	out, err := r.Call(ctx, "src_b", "go")
	if err != nil || string(out) != `"b"` {
		t.Fatalf("b = %s, %v", out, err)
	}
	if got := <-done; got != `"a"` {
		t.Fatalf("a = %s", got)
	}
	for _, req := range site.requests() {
		want := "comic:src_a"
		if strings.HasPrefix(req.URL, "https://b.test") {
			want = "comic:src_b"
		}
		if req.Partition != want {
			t.Fatalf("%s used partition %s", req.URL, req.Partition)
		}
	}
}

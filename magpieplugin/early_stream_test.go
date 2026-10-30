package magpieplugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moejs"
)

// Both interfaces are implemented so the old bridge is a useful negative
// control: it receives the same body, but cannot expose the head early.
type gatedHeadHost struct {
	first, tail         chan struct{}
	stopped             chan struct{}
	firstOnce, tailOnce sync.Once
	pieces              [][]byte
	failure             error
}

func newGatedHeadHost(t *testing.T) *gatedHeadHost {
	h := &gatedHeadHost{first: make(chan struct{}), tail: make(chan struct{}), stopped: make(chan struct{}), pieces: [][]byte{[]byte("first ")}}
	t.Cleanup(func() { h.firstOnce.Do(func() { close(h.first) }); h.tailOnce.Do(func() { close(h.tail) }) })
	return h
}
func (h *gatedHeadHost) Do(context.Context, *Request) (*Response, error) {
	return nil, errors.New("unexpected whole request")
}
func (h *gatedHeadHost) DoStream(ctx context.Context, req *Request, onChunk func([]byte) error) (*Response, error) {
	return h.DoStreamWithHead(ctx, req, func(*Response) error { return nil }, onChunk)
}
func (h *gatedHeadHost) DoStreamWithHead(ctx context.Context, req *Request, onHead func(*Response) error, onChunk func([]byte) error) (*Response, error) {
	defer close(h.stopped)
	head := &Response{Status: 201, Header: map[string][]string{"X-Stream": {"ready"}}, URL: req.URL + "/final"}
	if err := onHead(head); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.first:
	}
	for _, p := range h.pieces {
		if err := onChunk(p); err != nil {
			return head, err
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.tail:
	}
	if h.failure != nil {
		return nil, h.failure
	}
	return head, nil
}
func streamProbe(t *testing.T, h Host, source string) (*Instance, <-chan string) {
	t.Helper()
	b, err := NewBundle(map[string][]byte{"package.json": []byte(`{"main":"./index.mjs"}`), "index.mjs": []byte(source)})
	if err != nil {
		t.Fatal(err)
	}
	l, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	in, err := l.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)
	if err = in.UseHost(h); err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 8)
	if err = in.SetFunction("report", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		seen <- moejs.Arg(args, 0).String()
		return moejs.Undefined(), nil
	}); err != nil {
		t.Fatal(err)
	}
	return in, seen
}
func awaitStreamReport(t *testing.T, reports <-chan string, want string) {
	t.Helper()
	select {
	case got := <-reports:
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no %q before releasing the response body", want)
	}
}
func callStreamProbe(in *Instance) <-chan error {
	result := make(chan error, 1)
	go func() { _, err := in.Call("probe", nil); result <- err }()
	return result
}
func awaitStreamCall(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream call did not finish")
	}
}
func TestFetchExposesHeadAndFirstChunkBeforeBodyEnds(t *testing.T) {
	h := newGatedHeadHost(t)
	in, seen := streamProbe(t, h, `export async function probe() {
 const r = await fetch("https://fixture.test")
 report(r.status+":"+r.headers.get("x-stream")+":"+r.url)
 const reader=r.body.getReader()
 report(new TextDecoder().decode((await reader.read()).value))
 report((await reader.read()).done ? "done" : "unexpected")
 report(String(__magpieStreams.size))
}`)
	result := callStreamProbe(in)
	awaitStreamReport(t, seen, "201:ready:https://fixture.test/final")
	h.firstOnce.Do(func() { close(h.first) })
	awaitStreamReport(t, seen, "first ")
	select {
	case <-h.stopped:
		t.Fatal("host completed before body release")
	default:
	}
	h.tailOnce.Do(func() { close(h.tail) })
	awaitStreamReport(t, seen, "done")
	awaitStreamReport(t, seen, "0")
	awaitStreamCall(t, result)
}
func TestStreamAbortAfterFetchResolvesCancelsHost(t *testing.T) {
	for _, mode := range []string{"signal", "reader"} {
		t.Run(mode, func(t *testing.T) {
			h := newGatedHeadHost(t)
			h.firstOnce.Do(func() { close(h.first) })
			action := `controller.abort(); try { await reader.read(); report("wrong end") } catch(e) { report("aborted") }`
			if mode == "reader" {
				action = `await reader.cancel(); report("aborted")`
			}
			in, seen := streamProbe(t, h, `export async function probe(){const controller=new AbortController();const r=await fetch("https://fixture.test",{signal:controller.signal});const reader=r.body.getReader();await reader.read();`+action+`}`)
			result := callStreamProbe(in)
			awaitStreamReport(t, seen, "aborted")
			select {
			case <-h.stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("body cancellation did not stop host")
			}
			awaitStreamCall(t, result)
		})
	}
}
func TestStreamingTextPreservesSplitUTF8Bytes(t *testing.T) {
	h := newGatedHeadHost(t)
	raw := []byte("首段🙂")
	for i := range raw {
		h.pieces = append(h.pieces, raw[i:i+1])
	}
	h.firstOnce.Do(func() { close(h.first) })
	h.tailOnce.Do(func() { close(h.tail) })
	in, seen := streamProbe(t, h, `export async function probe(){ const r=await fetch("https://fixture.test"); report(await r.text()) }`)
	result := callStreamProbe(in)
	awaitStreamReport(t, seen, "first 首段🙂")
	awaitStreamCall(t, result)
}
func TestEarlyFetchReportsLaterBodyFailureToReader(t *testing.T) {
	h := newGatedHeadHost(t)
	h.failure = errors.New("connection lost after head")
	in, seen := streamProbe(t, h, `export async function probe(){const r=await fetch("https://fixture.test");report("head");try{await r.text();report("wrong end")}catch(e){report(e.message)}}`)
	result := callStreamProbe(in)
	awaitStreamReport(t, seen, "head")
	h.firstOnce.Do(func() { close(h.first) })
	h.tailOnce.Do(func() { close(h.tail) })
	awaitStreamReport(t, seen, "connection lost after head")
	awaitStreamCall(t, result)
}

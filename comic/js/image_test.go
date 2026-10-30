package js

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const imageSource = `class M extends ComicSource {
  name = 'M'; key = 'm'; version = '1'
  comic = {
    loadInfo: async (id) => ({id: id, title: 't', chapters: []}),
    // A source whose images are scrambled: the address carries a marker and
    // onResponse has to undo it, exactly what cxc and mojoin do.
    onImageLoad: (url, comic, chapter) => ({
      url: 'https://img.example/' + url,
      headers: {Referer: 'https://example/'},
      onResponse: (data) => {
        const view = new Uint8Array(data)
        for (let i = 0; i < view.length; i++) view[i] = 255 - view[i]
        return view.buffer
      },
    }),
    onThumbnailLoad: (url) => ({url: 'https://thumb.example/' + url}),
  }
}`

// A source that offers mirrors: the first address fails and the client is
// expected to ask for the next one.
const mirrorSource = `class F extends ComicSource {
  name = 'F'; key = 'f'; version = '1'
  comic = {
    onImageLoad: (url) => ({
      url: 'https://a.example/' + url,
      onLoadFailed: () => ({url: 'https://b.example/' + url, headers: {Referer: 'https://b/'}}),
    }),
  }
}`

func TestImageConfigCarriesACallableHandle(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), imageSource); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg, err := r.ImageConfig(ctx, "m", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://img.example/1.jpg" || cfg.Headers["Referer"] != "https://example/" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Handle == "" || len(cfg.Callbacks) != 1 || cfg.Callbacks[0] != "onResponse" {
		t.Fatalf("handle = %q callbacks = %v, want onResponse", cfg.Handle, cfg.Callbacks)
	}

	// The body goes through onResponse, which inverts every byte.
	body := []byte{0, 1, 250, 255}
	got, err := r.ApplyImageResponse(ctx, "m", cfg.Handle, body)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{255, 254, 5, 0}
	if string(got) != string(want) {
		t.Errorf("onResponse gave %v, want %v", got, want)
	}

	// A config without callbacks stays callable-free.
	thumb, err := r.ImageConfig(ctx, "m", "1.jpg", "c1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.URL != "https://thumb.example/1.jpg" {
		t.Errorf("thumbnail url = %q", thumb.URL)
	}
	if thumb.Handle != "" {
		t.Errorf("thumbnail handle = %q, want empty", thumb.Handle)
	}
	if _, err := r.ApplyImageResponse(ctx, "m", "i999", body); !errors.Is(err, ErrNoImageCallback) {
		t.Errorf("a handle nobody stored answered %v, want ErrNoImageCallback", err)
	}
}

func TestImageFallbackGivesTheNextMirror(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), mirrorSource); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg, err := r.ImageConfig(ctx, "f", "2.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Callbacks) != 1 || cfg.Callbacks[0] != "onLoadFailed" {
		t.Fatalf("callbacks = %v, want onLoadFailed", cfg.Callbacks)
	}
	next, err := r.ImageFallback(ctx, "f", cfg.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if next.URL != "https://b.example/2.jpg" || next.Headers["Referer"] != "https://b/" {
		t.Errorf("fallback = %+v", next)
	}

	// A bad handle is refused before it reaches the runtime.
	if _, err := r.ImageFallback(ctx, "f", "../../etc/passwd"); err == nil {
		t.Error("a malformed handle was accepted")
	}
}

func TestImageConfigOnASourceWithoutTheHook(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class N extends ComicSource { name='N'; key='n'; version='1' }`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ImageConfig(context.Background(), "n", "u", "c", "", false); !errors.Is(err, ErrNoImageCallback) {
		t.Fatalf("err = %v, want ErrNoImageCallback", err)
	}
}

func TestImageHandlesAreBounded(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), imageSource); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := r.ImageConfig(ctx, "m", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Past the bound the oldest handles are dropped, so a long reading session
	// cannot keep every config alive.
	for i := 0; i <= imageMaxHandles; i++ {
		if _, err := r.ImageConfig(ctx, "m", "p.jpg", "c1", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.ApplyImageResponse(ctx, "m", first.Handle, []byte{1}); !errors.Is(err, ErrNoImageCallback) {
		t.Errorf("the oldest handle still answered (%v), want it dropped", err)
	}
	// A fresh config still works.
	again, err := r.ImageConfig(ctx, "m", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyImageResponse(ctx, "m", again.Handle, []byte{0}); err != nil {
		t.Errorf("a new handle failed: %v", err)
	}
}

func TestImageResponseRequiresBytes(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := strings.Replace(imageSource, "return view.buffer", "return 'not bytes'", 1)
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg, err := r.ImageConfig(ctx, "m", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyImageResponse(ctx, "m", cfg.Handle, []byte{1, 2}); err == nil {
		t.Error("a non-byte result was accepted")
	}
}

// modifyImage is a string of JavaScript, not a function: it must survive as
// text and must not be reported as a callback.
func TestImageConfigCarriesTheModifyScript(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class S extends ComicSource {
  name='S'; key='s'; version='1'
  comic = {
    onImageLoad: (url) => ({
      url: 'https://img.example/' + url,
      modifyImage: "let modifyImage = (image) => image.copyAndRotate90()",
    }),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg, err := r.ImageConfig(ctx, "s", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModifyImage == "" || !strings.Contains(cfg.ModifyImage, "copyAndRotate90") {
		t.Fatalf("modifyImage = %q", cfg.ModifyImage)
	}
	if cfg.Handle != "" || len(cfg.Callbacks) != 0 {
		t.Errorf("handle = %q callbacks = %v, want none", cfg.Handle, cfg.Callbacks)
	}
	// The script Core hands over is the script the runner accepts.
	got, err := r.ModifyImage(ctx, cfg.ModifyImage, testBitmap())
	if err != nil {
		t.Fatal(err)
	}
	if got.W != 2 || got.H != 2 {
		t.Errorf("result = %dx%d", got.W, got.H)
	}
}

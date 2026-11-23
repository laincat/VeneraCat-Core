package gohost

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A source may attach a modifyImage script to an image: jm reorders the blocks
// of a long strip that way and several sources rotate a page. The script runs
// over the decoded picture, so what comes back must be the script's output, not
// the bytes the server sent.
func TestModifyImageScriptIsApplied(t *testing.T) {
	// A 4x2 picture: red column first, blue column second.
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if x < 2 {
				src.Set(x, y, color.RGBA{R: 255, A: 255})
			} else {
				src.Set(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	var body bytes.Buffer
	if err := png.Encode(&body, src); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Rot extends ComicSource {
    name = "Rot"; key = "rot"; version = "1.0.0"
    comic = {
        onImageLoad: (url) => ({
            url: url,
            modifyImage: "let modifyImage = (image) => image.copyAndRotate90()",
        })
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	got, ctype, err := c.FetchImage(context.Background(), "rot", srv.URL+"/1.png", "c1", "e1")
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	// The script turned the 4x2 picture into a 2x4 one.
	if ctype != "image/png" {
		t.Errorf("content type = %q, want image/png after re-encoding", ctype)
	}
	out, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("the result is not a PNG: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 4 {
		t.Fatalf("result is %dx%d, want 2x4", b.Dx(), b.Dy())
	}
	// A clockwise quarter turn puts the red column on top.
	r, _, _, _ := out.At(0, 0).RGBA()
	if r < 0xF000 {
		t.Errorf("the top pixel is %v, want red", out.At(0, 0))
	}
}

// A script that cannot produce an image must fail loudly rather than hand the
// original bytes over as if the source's instruction had been honoured.
func TestModifyImageFailureIsReported(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var body bytes.Buffer
	if err := png.Encode(&body, src); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Bad extends ComicSource {
    name = "Bad"; key = "bad"; version = "1.0.0"
    comic = {
        onImageLoad: (url) => ({ url: url, modifyImage: "let modifyImage = () => 'nope'" })
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, _, err := c.FetchImage(context.Background(), "bad", srv.URL+"/1.png", "c1", "e1"); err == nil {
		t.Error("a script that returns no image was accepted")
	}
}

// A source may pair a pixel script with a format this build cannot read —
// hitomi serves avif. The script cannot run, but failing the page over it would
// hide an image the reader could show as it arrived.
func TestModifyImagePassesThroughAnUnknownFormat(t *testing.T) {
	avif := append([]byte{0, 0, 0, 0x1C, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'}, make([]byte, 40)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/avif")
		_, _ = w.Write(avif)
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Avif extends ComicSource {
    name = "Avif"; key = "avif"; version = "1.0.0"
    comic = {
        onImageLoad: (url) => ({ url: url, modifyImage: "let modifyImage = (i) => i" })
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, ctype, err := c.FetchImage(context.Background(), "avif", srv.URL+"/1.avif", "c1", "e1")
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if !bytes.Equal(got, avif) {
		t.Errorf("the body changed: %d bytes", len(got))
	}
	if ctype != "image/avif" {
		t.Errorf("content type = %q, want the server's own", ctype)
	}
}

// The decoders that must be registered are easy to forget: with only png
// registered, a jpeg page silently took the "cannot decode" path and the
// script never ran. The type of the result is what proves it ran.
func TestModifyImageRunsOnJPEGBytes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.Set(x, y, color.RGBA{G: 200, A: 255})
		}
	}
	var body bytes.Buffer
	if err := jpeg.Encode(&body, src, nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Jpg extends ComicSource {
    name = "Jpg"; key = "jpg"; version = "1.0.0"
    comic = {
        onImageLoad: (url) => ({ url: url, modifyImage: "let modifyImage = (image) => image.copyAndRotate90()" })
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, ctype, err := c.FetchImage(context.Background(), "jpg", srv.URL+"/1.jpg", "c1", "e1")
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if ctype != "image/png" {
		t.Fatalf("content type = %q, want image/png: the script did not run", ctype)
	}
	out, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
		t.Errorf("result is %dx%d, want 2x3", b.Dx(), b.Dy())
	}
}

// A cover ignores the script: the official client documents modifyImage as
// unsupported for thumbnails, and a cover is small.
func TestModifyImageIsIgnoredForCovers(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Cover extends ComicSource {
    name = "Cover"; key = "cover"; version = "1.0.0"
    comic = {
        onImageLoad: (url) => ({ url: url, modifyImage: "let modifyImage = (i) => i",
                                 onResponse: (d) => d, onLoadFailed: () => ({url: url}) }),
        onThumbnailLoad: (url) => ({ url: url, modifyImage: "let modifyImage = (i) => i",
                                     onResponse: (d) => d, onLoadFailed: () => ({url: url}) }),
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx := context.Background()
	for _, thumbnail := range []bool{true, false} {
		req, err := c.ImageRequest(ctx, "cover", "https://img.test/1.jpg", "c1", "e1", thumbnail)
		if err != nil {
			t.Fatalf("request (thumbnail=%v): %v", thumbnail, err)
		}
		if thumbnail {
			if req.ModifyImage != "" {
				t.Errorf("a cover request carries a pixel script")
			}
			if req.HasCallback("onLoadFailed") {
				t.Errorf("a cover request carries a mirror fallback")
			}
			// onResponse must survive: a scrambled cover is still readable.
			if !req.HasCallback("onResponse") {
				t.Errorf("a cover request lost onResponse")
			}
		}
		if !thumbnail && req.ModifyImage == "" {
			t.Errorf("a page request lost its pixel script")
		}
		if !thumbnail && !req.HasCallback("onLoadFailed") {
			t.Errorf("a page request lost its mirror fallback")
		}
	}
}

// A tag's meaning belongs to the source: one searches "作者:<name>", another
// maps 类型 onto a category parameter, and a third refuses outright.
func TestClickTagReadsWhatTheSourceWants(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	script := `
class Tags extends ComicSource {
    name = "Tags"; key = "tags"; version = "1.0.0"
    comic = {
        onClickTag: (namespace, tag) => {
            if (namespace === "作者") return {action: "search", keyword: namespace + ":" + tag, param: null}
            if (namespace === "类型") return {action: "category", keyword: tag, param: "33"}
            throw "未支持此类Tag检索"
        }
    }
}
`
	if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx := context.Background()
	got, err := c.ClickTag(ctx, "tags", "作者", "张三")
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "search" || got.Keyword != "作者:张三" || got.Param != "" {
		t.Errorf("author tag = %+v", got)
	}
	got, err = c.ClickTag(ctx, "tags", "类型", "长篇")
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "category" || got.Param != "33" || got.Keyword != "长篇" {
		t.Errorf("category tag = %+v", got)
	}
	// A source that refuses the namespace says so; the message is what the
	// interface shows.
	if _, err := c.ClickTag(ctx, "tags", "语言", "中文"); err == nil || !strings.Contains(err.Error(), "Tag检索") {
		t.Errorf("an unsupported tag answered %v", err)
	}
	// A source with no hook at all is distinguishable from one that refused.
	if _, err := c.ClickTag(ctx, "missing", "作者", "张三"); !errors.Is(err, ErrNoTagAction) {
		t.Errorf("a source without the hook answered %v", err)
	}
}

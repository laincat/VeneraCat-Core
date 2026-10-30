package mobile

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A source whose images are scrambled and whose first mirror may fail — the
// two shapes a plain url/method/headers answer cannot express.
const imageComic = `
class Img extends ComicSource {
    name = "Img"; key = "img"; version = "1.0.0"
    comic = {
        onImageLoad: (url, comic, chapter) => ({
            url: "https://img.test/" + url,
            headers: {Referer: "https://img.test/"},
            onResponse: (data) => {
                const view = new Uint8Array(data)
                for (let i = 0; i < view.length; i++) view[i] = 255 - view[i]
                return view.buffer
            },
            onLoadFailed: () => ({url: "https://mirror.test/" + url}),
        }),
        onThumbnailLoad: (url) => ({url: "https://thumb.test/" + url}),
    }
}
`

func TestImageCallbacksThroughMobile(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	if _, err := c.Install(0, "comic", imageComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	out, err := c.ImageConfig("img", "1.jpg", "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		Handle    string            `json:"handle"`
		Callbacks []string          `json:"callbacks"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://img.test/1.jpg" || cfg.Headers["Referer"] != "https://img.test/" {
		t.Fatalf("config = %s", out)
	}
	if cfg.Handle == "" || len(cfg.Callbacks) != 2 {
		t.Fatalf("handle = %q callbacks = %v, want two", cfg.Handle, cfg.Callbacks)
	}

	// onResponse runs over the body; the method speaks base64 so bytes of any
	// kind survive gomobile.
	got, err := c.ApplyImageResponse("img", cfg.Handle, base64.StdEncoding.EncodeToString([]byte{0, 1, 250, 255}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string([]byte{255, 254, 5, 0}) {
		t.Errorf("onResponse gave %v", raw)
	}

	next, err := c.ImageFallback("img", cfg.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "mirror.test") {
		t.Errorf("fallback = %s", next)
	}

	// A cover asks the other hook, and a config without callbacks has none.
	thumb, err := c.ImageConfig("img", "1.jpg", "c1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(thumb, "thumb.test") {
		t.Errorf("thumbnail = %s", thumb)
	}
	var thumbCfg struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal([]byte(thumb), &thumbCfg); err != nil || thumbCfg.Handle != "" {
		t.Errorf("thumbnail handle = %q, %v", thumbCfg.Handle, err)
	}

	// A source that has no such callback reports it, rather than failing with
	// a script error the caller cannot act on.
	if _, err := c.ApplyImageResponse("img", "i999", base64.StdEncoding.EncodeToString([]byte{1})); err == nil {
		t.Error("a handle nobody stored answered")
	} else if !strings.Contains(err.Error(), "no such callback") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.ApplyImageResponse("img", cfg.Handle, "not base64!!"); err == nil {
		t.Error("a body that is not base64 was accepted")
	}
}

// A pixel-buffer script crosses gomobile as base64 and must come back as the
// pixels the script produced, with the source's own geometry.
func TestModifyImageThroughMobile(t *testing.T) {
	c := newCore(t, &fakeHost{routes: map[string]string{}, data: map[string]string{}})
	if _, err := c.Install(0, "comic", imageComic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	// A 2x2 picture: first pixel red, last blue.
	rgba := []byte{
		255, 0, 0, 255, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 255, 255,
	}
	out, err := c.ModifyImage("img", "let modifyImage = (image) => image.copyAndRotate90()", 2, 2,
		base64.StdEncoding.EncodeToString(rgba))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		RGBA   string `json:"rgba"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Width != 2 || got.Height != 2 {
		t.Fatalf("size = %dx%d", got.Width, got.Height)
	}
	pix, err := base64.StdEncoding.DecodeString(got.RGBA)
	if err != nil || len(pix) != len(rgba) {
		t.Fatalf("pixels = %d bytes, %v", len(pix), err)
	}
	// A clockwise turn puts the red pixel top-right.
	if pix[4] != 255 || pix[6] != 0 {
		t.Errorf("rotation did not move the red pixel: %v", pix)
	}

	// A script that yields no image is reported, not silently passed through.
	if _, err := c.ModifyImage("img", "let modifyImage = () => 1", 2, 2, base64.StdEncoding.EncodeToString(rgba)); err == nil {
		t.Error("a script that returns no image was accepted")
	}
	// Pixels that do not match the stated size are refused.
	if _, err := c.ModifyImage("img", "let modifyImage = (i) => i", 3, 3, base64.StdEncoding.EncodeToString(rgba)); err == nil {
		t.Error("a short pixel buffer was accepted")
	}
}

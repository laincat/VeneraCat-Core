package gohost

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// onLoadFailed returns a new ImageLoadingConfig. A mirror can use a different
// HTTP method/body and pixel transform from the failed request.
func TestImageFallbackReplacesRequestAndPixelScript(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			picture.Set(x, y, color.RGBA{R: 240, G: 80, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	const rotate = `"let modifyImage = (image) => image.copyAndRotate90()"`
	for _, tc := range []struct {
		name, initialExtra, fallbackExtra string
		method, body                      string
		width, height                     int
		invert                            bool
	}{
		{
			name:         "omitted fields do not inherit the failed request",
			initialExtra: `method: "POST", data: "original-request-body", headers: {"X-Original": "original"}, modifyImage: ` + rotate + `,`,
			method:       "GET", width: 4, height: 2,
		},
		{
			name: "replacement callback and pixel script both run",
			fallbackExtra: `method: "post", data: "mirror-request-body", modifyImage: ` + rotate + `,
                onResponse: (data) => {
                    let a = new Uint8Array(data);
                    for (let i = 0; i < a.length; i++) a[i] = 255 - a[i];
                    return a.buffer;
                },`,
			method: "POST", body: "mirror-request-body", width: 2, height: 4, invert: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type observed struct{ path, method, body, originalHeader string }
			requests := make(chan observed, 4)
			wire := append([]byte(nil), encoded.Bytes()...)
			if tc.invert {
				for i := range wire {
					wire[i] = 255 - wire[i]
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests <- observed{r.URL.Path, r.Method, string(body), r.Header.Get("X-Original")}
				if r.URL.Path == "/first" {
					http.Error(w, "first mirror unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(wire)
			}))
			defer server.Close()
			c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.0", Locale: "zh_CN"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			script := fmt.Sprintf(`class Mirror extends ComicSource {
                name = "Mirror"; key = "mirror"; version = "1.0.0";
                comic = {onImageLoad: (url) => ({
                    url: url, %s
                    onLoadFailed: () => ({url: %q, %s}),
                })};
            }`, tc.initialExtra, server.URL+"/mirror", tc.fallbackExtra)
			if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
				t.Fatal(err)
			}
			got, _, err := c.FetchImage(context.Background(), "mirror", server.URL+"/first", "comic", "chapter")
			if err != nil {
				t.Fatalf("fetch fallback image: %v", err)
			}
			if len(requests) != 2 {
				t.Fatalf("HTTP request count = %d, want 2", len(requests))
			}
			first, mirror := <-requests, <-requests
			if first.path != "/first" || mirror.path != "/mirror" {
				t.Fatalf("request sequence = %+v then %+v", first, mirror)
			}
			if mirror.method != tc.method || mirror.body != tc.body || mirror.originalHeader != "" {
				t.Errorf("mirror request = %+v, want method=%s body=%q without original header", mirror, tc.method, tc.body)
			}
			decoded, err := png.Decode(bytes.NewReader(got))
			if err != nil {
				t.Fatalf("decode fallback image: %v", err)
			}
			if b := decoded.Bounds(); b.Dx() != tc.width || b.Dy() != tc.height {
				t.Errorf("fallback dimensions = %dx%d, want %dx%d", b.Dx(), b.Dy(), tc.width, tc.height)
			}
		})
	}
}

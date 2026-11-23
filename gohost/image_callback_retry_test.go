package gohost

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Image loading includes the source's response and pixel transforms. A failure
// there must reach onLoadFailed just as a failed HTTP request does.
func TestImageTransformFailuresFollowMirrors(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, transform string
		mirror          bool
	}{
		{"response exception", `onResponse: () => {throw new Error("source transform failure")},`, true},
		{"invalid response result", `onResponse: () => "not bytes",`, true},
		{"pixel exception", `modifyImage: "let modifyImage = () => {throw new Error('pixel transform failure')}",`, true},
		{"response error without mirror", `onResponse: () => {throw new Error("source transform failure")},`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(encoded.Bytes())
			}))
			defer server.Close()
			c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.0", Locale: "zh_CN"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fallback := ""
			if tc.mirror {
				fallback = fmt.Sprintf(`onLoadFailed: () => ({url: %q}),`, server.URL+"/mirror")
			}
			script := fmt.Sprintf(`class Retry extends ComicSource {
                name = "Retry"; key = "retry"; version = "1.0.0";
                comic = {onImageLoad: (url) => ({url, %s %s})};
            }`, tc.transform, fallback)
			if _, err := c.Install(context.Background(), script, "test", ""); err != nil {
				t.Fatal(err)
			}
			got, _, err := c.FetchImage(context.Background(), "retry", server.URL+"/first", "comic", "chapter")
			if !tc.mirror {
				if err == nil || !strings.Contains(err.Error(), "source transform failure") {
					t.Fatalf("missing original source error: %v", err)
				}
				if len(paths) != 1 || <-paths != "/first" {
					t.Fatal("unexpected retry without an onLoadFailed callback")
				}
				return
			}
			if err != nil {
				t.Fatalf("transform failure did not reach the mirror: %v", err)
			}
			if len(paths) != 2 || <-paths != "/first" || <-paths != "/mirror" {
				t.Fatal("did not fetch the source-selected mirror exactly once")
			}
			if !bytes.Equal(got, encoded.Bytes()) {
				t.Fatal("mirror response bytes changed")
			}
			decoded, err := png.Decode(bytes.NewReader(got))
			if err != nil || decoded.Bounds().Dx() != 8 || decoded.Bounds().Dy() != 4 {
				t.Fatalf("mirror did not return the expected decodable image: %v", err)
			}
		})
	}
}

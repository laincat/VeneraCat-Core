package gohost

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
)

func TestTransportDecodesExplicitCompression(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept-Encoding"); got != "gzip, deflate" {
					t.Errorf("Accept-Encoding = %q", got)
				}
				w.Header().Set("Content-Encoding", encoding)
				var writer io.WriteCloser
				if encoding == "gzip" {
					writer = gzip.NewWriter(w)
				} else {
					writer = zlib.NewWriter(w)
				}
				_, _ = writer.Write([]byte(`{"ok":true}`))
				_ = writer.Close()
			}))
			defer srv.Close()
			tr := NewTransport(nil, "test", time.Second)
			resp, err := tr.Do(context.Background(), &host.Request{URL: srv.URL, Header: http.Header{"Accept-Encoding": {"gzip, deflate, br, zstd"}}})
			if err != nil {
				t.Fatal(err)
			}
			if string(resp.Body) != `{"ok":true}` {
				t.Fatalf("body = %q", resp.Body)
			}
			if resp.Header.Get("Content-Encoding") != "" {
				t.Fatal("decoded response retains content encoding")
			}
		})
	}
}

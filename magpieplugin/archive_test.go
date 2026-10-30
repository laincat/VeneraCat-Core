package magpieplugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"
)

// zipOf builds a zip in memory, optionally under an enclosing directory.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarGzOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A package installed from a downloaded archive has to end up as the same file
// table a folder install produces, or the two paths would disagree about what a
// package is.
func TestArchivesProduceTheSameTable(t *testing.T) {
	want := map[string]string{
		"package.json": `{"name":"demo","version":"0.0.0","main":"./index.mjs"}`,
		"index.mjs":    "export async function Plugin() { return {} }\n",
		"lib/x.mjs":    "export const x = 1\n",
	}
	for _, kind := range []string{"zip", "targz", "zip-with-dir", "targz-with-dir"} {
		t.Run(kind, func(t *testing.T) {
			files := map[string]string{}
			for name, body := range want {
				if strings.HasSuffix(kind, "with-dir") {
					name = "demo-0.0.0/" + name
				}
				files[name] = body
			}
			var data []byte
			if strings.HasPrefix(kind, "zip") {
				data = zipOf(t, files)
			} else {
				data = tarGzOf(t, files)
			}
			got, err := ReadArchive(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(want), keys(got))
			}
			for name, body := range want {
				if string(got[name]) != body {
					t.Fatalf("%s = %q", name, got[name])
				}
			}
			// And the table has to satisfy the bundle, not just look right: a
			// table that cannot be loaded has not solved installation.
			if _, err := NewBundle(got); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(mustBundle(t, got)); err != nil {
				t.Fatalf("extracted package does not load: %v", err)
			}
		})
	}
}

func mustBundle(t *testing.T, files map[string][]byte) *Bundle {
	t.Helper()
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A link has no meaning once the package lives in memory, and following one
// would let an archive point at anything on disk.
func TestArchivesRefuseLinksAndTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "demo/index.mjs", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if _, err := ReadArchive(buf.Bytes()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink: err = %v", err)
	}

	zipData := zipOf(t, map[string]string{"../escape.mjs": "x"})
	if _, err := ReadArchive(zipData); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal: err = %v", err)
	}
}

func TestReadArchiveRejectsOtherBytes(t *testing.T) {
	if _, err := ReadArchive([]byte("just text")); !errors.Is(err, ErrNotAnArchive) {
		t.Fatalf("err = %v", err)
	}
}

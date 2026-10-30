package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/subscription"
)

func TestDuplicateScriptInstallReportsExistingPlugin(t *testing.T) {
	for _, item := range []struct {
		name      string
		typ       Type
		body, key string
	}{
		{"comic", TypeComic, comicV1, "c1"},
		{"video", TypeVideoJS, videoJS, "v1"},
	} {
		t.Run(item.name, func(t *testing.T) {
			r := newRegistry(t)
			ctx := context.Background()
			first, err := r.Install(ctx, item.typ, item.body, "original.js", "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{true, false} {
				if err := r.SetEnabled(ctx, item.key, enabled); err != nil {
					t.Fatal(err)
				}
				_, err := r.Install(ctx, item.typ, item.body, "replacement.js", "", "")
				if !errors.Is(err, ErrExists) {
					t.Errorf("enabled=%v: expected ErrExists, got %v", enabled, err)
				}
				var collision *KeyExistsError
				if !errors.As(err, &collision) || collision.Key != item.key {
					t.Errorf("missing structured key: %v", err)
				}
				after, ok := r.Get(item.key)
				if !ok || after.Body != first.Body || after.Origin != first.Origin || after.Enabled != enabled {
					t.Fatalf("duplicate changed installed record: %+v", after)
				}
			}
		})
	}
}

func TestDuplicateLoaderRetainsLegacyClassification(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.js.LoadSource(ctx, comicV2)
	var collision *js.DuplicateKeyError
	var loadError *js.LoadError
	if !errors.As(err, &collision) || collision.Key != "c1" || collision.Kind != js.KindComic || !errors.As(err, &loadError) {
		t.Fatalf("loader error contract: %v", err)
	}
	if err.Error() != "js: invalid source: key(c1) already exists" {
		t.Fatalf("legacy message changed: %v", err)
	}
}

func TestRuntimeOnlyKeyIsNotOfferedAsRegistryUpdate(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.js.LoadSource(ctx, comicV1); err != nil {
		t.Fatal(err)
	}
	_, err := r.Install(ctx, TypeComic, comicV2, "", "", "")
	if err == nil || errors.Is(err, ErrExists) || len(r.List()) != 0 {
		t.Fatalf("unowned runtime key reported as installed plugin: %v", err)
	}
}

func TestExistingKeyFromIndexesAndBuiltins(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.EnableBuiltin("builtin_hongguo"); err != nil {
		t.Fatal(err)
	}
	_, err := r.EnableBuiltin("builtin_hongguo")
	var collision *KeyExistsError
	if !errors.Is(err, ErrExists) || !errors.As(err, &collision) || collision.Key != "builtin_hongguo" {
		t.Fatalf("builtin collision: %v", err)
	}
	if _, err := r.Install(ctx, TypeComic, comicV1, "", "", ""); err != nil {
		t.Fatal(err)
	}
	_, err = r.InstallFromComicIndex(ctx, "https://index.test/index.json", subscription.Entry{Key: "c1", URL: "https://index.test/c.js"}, comicV2)
	if !errors.As(err, &collision) || collision.Key != "c1" {
		t.Fatalf("index collision: %v", err)
	}
	failed := r.Restore(ctx, r.List())
	if len(failed) != 2 {
		t.Fatalf("restore collisions: %v", failed)
	}
	for key, err := range failed {
		if !errors.As(err, &collision) || collision.Key != key {
			t.Fatalf("restore collision %s: %v", key, err)
		}
	}
}

func TestScriptExceptionCannotImpersonateExistingPlugin(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "original.js", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`class Fake extends ComicSource { constructor(){ super(); throw new Error("js: invalid source: key(c1) already exists") } }`,
		`class Fake extends ComicSource { name="Fake"; key="new_key"; version="1"; init(){ throw new Error("plugin: key already installed: c1") } }`,
	} {
		_, err := r.Install(ctx, TypeComic, body, "bad.js", "", "")
		if err == nil || errors.Is(err, ErrExists) {
			t.Fatalf("script exception treated as duplicate: %v", err)
		}
	}
	if rec, _ := r.Get("c1"); rec.Body != comicV1 || rec.Origin != "original.js" {
		t.Fatalf("original changed: %+v", rec)
	}
}

func TestWrongScriptKindCannotBecomeUpdateSignal(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()
	if _, err := r.Install(ctx, TypeComic, comicV1, "original.js", "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.Install(ctx, TypeVideoJS, comicV1, "wrong-kind.js", "", "")
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatalf("wrong kind was offered as an update: %v", err)
	}
	if rec, _ := r.Get("c1"); rec.Type != TypeComic || rec.Body != comicV1 {
		t.Fatal("original overwritten")
	}
	// A different source family may legitimately collide in the shared key
	// namespace. It is still a duplicate; Replace must enforce the old type.
	body := strings.Replace(videoJS, "key = 'v1'", "key = 'c1'", 1)
	_, err = r.Install(ctx, TypeVideoJS, body, "video.js", "", "")
	if !errors.Is(err, ErrExists) {
		t.Errorf("cross-family key collision: %v", err)
	}
	if _, err = r.Replace(ctx, "c1", TypeVideoJS, body, "video.js", "", ""); !errors.Is(err, ErrWrongUse) {
		t.Errorf("cross-family replacement: %v", err)
	}
}

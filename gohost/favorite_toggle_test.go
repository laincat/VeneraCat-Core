package gohost

import (
	"context"
	"encoding/json"
	"testing"
)

// Adding and removing reach the source with what it expects: the comic, the
// folder its loader wants, and whether this is an add. A single-folder source
// takes no folder at all.
func TestSetFavoritePassesWhatTheSourceExpects(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	if _, err := c.Install(ctx, favoritesComic, "file_fav2", ""); err != nil {
		t.Fatal(err)
	}
	// A token is required by the source, so the first attempt is refused and
	// proves the arguments reached it.
	if err := c.SetFavorite(ctx, "fav", "c1", "f1", "", true); err == nil {
		t.Fatal("expected the source to refuse")
	}

	const recorder = `class Rec extends ComicSource {
  name = 'Rec'; key = 'rec'; version = '1'
  favorites = {
    multiFolder: false,
    addOrDelFavorite: async (comicId, folderId, isAdding) => {
      this.saveData('last', JSON.stringify([comicId, folderId, isAdding]))
      return 'ok'
    },
    loadComics: async () => ({comics: [], maxPage: 1}),
  }
}`
	if _, err := c.Install(ctx, recorder, "file_rec", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFavorite(ctx, "rec", "comic-9", "", "", true); err != nil {
		t.Fatalf("add: %v", err)
	}
	// The single-folder source must see null, not an empty string.
	if got := recordedArguments(t, c, "rec"); got != `["comic-9",null,true]` {
		t.Errorf("arguments = %s", got)
	}
	if err := c.SetFavorite(ctx, "rec", "comic-9", "", "", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := recordedArguments(t, c, "rec"); got != `["comic-9",null,false]` {
		t.Errorf("arguments = %s", got)
	}
}

// recordedArguments is what the recorder source saved, unquoted: saveData stores
// a JSON value, and a string arrives wrapped in another pair of quotes.
func recordedArguments(t *testing.T, c *Core, key string) string {
	t.Helper()
	raw, ok, err := c.Store().LoadData(key, "last")
	if err != nil || !ok {
		t.Fatalf("recorded: %v %v", ok, err)
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err == nil {
		return inner
	}
	return string(raw)
}

// Six official sources take a fourth argument: their own handle for the favorite
// entry. wnacg builds its delete URL from it, so a removal without one asks for
// "del-id-undefined" and does nothing while reporting success.
func TestSetFavoritePassesTheSourcesHandle(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	const handleComic = `class Handle extends ComicSource {
  name = 'H'; key = 'h'; version = '1'
  favorites = {
    multiFolder: false,
    addOrDelFavorite: async (comicId, folderId, isAdding, favoriteId) => {
      this.saveData('last', JSON.stringify([comicId, folderId, isAdding, favoriteId]))
      return 'ok'
    },
    loadComics: async () => ({comics: [], maxPage: 1}),
  }
}`
	if _, err := c.Install(ctx, handleComic, "file_h", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFavorite(ctx, "h", "c1", "", "fav-77", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := recordedArguments(t, c, "h"); got != `["c1",null,false,"fav-77"]` {
		t.Errorf("arguments = %s", got)
	}
	// A source that does not use the handle still sees null in that place.
	if err := c.SetFavorite(ctx, "h", "c1", "f2", "", true); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := recordedArguments(t, c, "h"); got != `["c1","f2",true,null]` {
		t.Errorf("arguments = %s", got)
	}
}

// A comic carries the handle from the page that listed it, so a removal knows
// which entry to delete.
func TestComicKeepsTheFavoriteHandle(t *testing.T) {
	raw := []byte(`{"id":"c1","title":"t","favoriteId":"fav-9"}`)
	comic := parseComic(raw)
	if comic.FavoriteID != "fav-9" {
		t.Errorf("FavoriteID = %q", comic.FavoriteID)
	}
	// A source that does not provide one leaves it empty rather than guessing.
	if got := parseComic([]byte(`{"id":"c2","title":"t"}`)); got.FavoriteID != "" {
		t.Errorf("FavoriteID = %q, want empty", got.FavoriteID)
	}
}

// loadInfo may report the handle too, so the details page works even when the
// comic came from a page that did not carry it.
func TestDetailsKeepTheFavoriteHandle(t *testing.T) {
	d, err := parseDetails([]byte(`{"title":"t","favoriteId":"fav-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.FavoriteID != "fav-5" {
		t.Errorf("FavoriteID = %q", d.FavoriteID)
	}
}

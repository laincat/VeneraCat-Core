package gohost

import (
	"context"
	"strings"
	"testing"
)

const favoritesComic = `class Fav extends ComicSource {
  name = 'Fav'; key = 'fav'; version = '1'
  favorites = {
    multiFolder: true,
    addOrDelFavorite: async (comicId, folderId, isAdding) => {
      if (!this.loadData('token')) { throw 'Login expired' }
      return isAdding ? 'added' : 'removed'
    },
    loadFolders: async (comicId) => ({
      folders: {f1: '想读', f2: '已读'},
      favorited: comicId ? ['f1'] : null,
    }),
    loadComics: async (page, folder) => ({
      comics: [{id: folder + '/' + page, title: 't', cover: ''}],
      maxPage: 3,
    }),
  }
}`

// A source keeps its own favorites, in one folder or several, and says which of
// them already hold a comic. Without reading that the client only ever saw its
// local shelf.
func TestFavoritesShapeIsRead(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	if _, err := c.Install(ctx, favoritesComic, "file_fav", ""); err != nil {
		t.Fatal(err)
	}
	got := c.FavoritesSupport(ctx, "fav")
	if !got.Supported || !got.MultiFolder || !got.Folders || !got.CanLoad {
		t.Fatalf("support = %+v", got)
	}

	folders, favorited, err := c.FavoriteFolders(ctx, "fav", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 || folders["f1"] != "想读" {
		t.Errorf("folders = %v", folders)
	}
	if len(favorited) != 1 || favorited[0] != "f1" {
		t.Errorf("favorited = %v", favorited)
	}

	page, err := c.FavoriteComics(ctx, "fav", "f2", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Comics) != 1 || page.MaxPage != 3 {
		t.Errorf("page = %+v", page)
	}

	// A source that refuses says so in its own words.
	if err := c.SetFavorite(ctx, "fav", "c1", "f1", "", true); err == nil {
		t.Error("adding without a token must fail")
	} else if !strings.Contains(err.Error(), "Login expired") {
		t.Errorf("error = %v, want the source's own text", err)
	}

	// A source with no favorites block supports none of it.
	plain := `class P extends ComicSource {
  name = 'P'; key = 'p'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := c.Install(ctx, plain, "file_p", ""); err != nil {
		t.Fatal(err)
	}
	if got := c.FavoritesSupport(ctx, "p"); got.Supported || got.Folders || got.CanLoad {
		t.Errorf("plain = %+v", got)
	}
}

// ehentai declares favorites without loadComics: it offers folders and a toggle
// but no list, so an entry that would show nothing must not appear.
func TestFavoritesWithoutAList(t *testing.T) {
	c := openTest(t)
	ctx := context.Background()
	const toggleOnly = `class T extends ComicSource {
  name = 'T'; key = 't'; version = '1'
  favorites = {
    multiFolder: true,
    addOrDelFavorite: async () => 'ok',
    loadFolders: async () => ({folders: {f1: '收藏'}, favorited: null}),
  }
}`
	if _, err := c.Install(ctx, toggleOnly, "file_t", ""); err != nil {
		t.Fatal(err)
	}
	got := c.FavoritesSupport(ctx, "t")
	if !got.Supported || !got.Folders {
		t.Errorf("support = %+v", got)
	}
	if got.CanLoad {
		t.Error("a source without loadComics must not report a list")
	}
}

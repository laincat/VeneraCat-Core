package gohost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const commentsSource = `
class Talk extends ComicSource {
  name = 'Talk'; key = 'talk'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
  comic = {
    loadInfo: async (id) => ({title: 'T', subId: 'sub-9', chapters: {}}),
    loadEp: async () => ({images: []}),
    loadComments: async (comicId, subId, page, replyTo) => ({
      comments: [
        {id: 'c1', userName: 'ada', content: 'first ' + comicId + '/' + subId + '/p' + page + '/' + (replyTo ?? 'none'), time: '昨天', score: '3', isLiked: true, replyCount: 2},
        {userName: 'bob', content: 'no id here'}
      ],
      maxPage: 4
    }),
    sendComment: async () => 'ok',
    likeComment: async () => 'ok',
  }
}`

func commentsCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "d"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Install(context.Background(), commentsSource, "file", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCommentsRoundTrip(t *testing.T) {
	c := commentsCore(t)
	ctx := context.Background()
	if !c.CommentsSupported(ctx, "talk") || !c.CanSendComment(ctx, "talk") || !c.CanLikeComment(ctx, "talk") {
		t.Fatal("the declared comment functions are not reported")
	}
	page, err := c.Comments(ctx, "talk", "42", "sub-9", 2, "")
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	if page.MaxPage != 4 || len(page.Comments) != 2 {
		t.Fatalf("page = %+v", page)
	}
	first := page.Comments[0]
	// The arguments reached the source in the official order: comic, subId, page,
	// replyTo — with null, not "", where the caller had nothing.
	if first.Content != "first 42/sub-9/p2/none" {
		t.Errorf("content = %q", first.Content)
	}
	if first.Score != "3" || !first.IsLiked || first.ReplyCount != 2 {
		t.Errorf("vote fields = %+v", first)
	}
	// A comment without an id is still readable.
	if page.Comments[1].UserName != "bob" {
		t.Errorf("second = %+v", page.Comments[1])
	}
	if err := c.SendComment(ctx, "talk", "42", "sub-9", "hello", ""); err != nil {
		t.Errorf("send: %v", err)
	}
	if err := c.LikeComment(ctx, "talk", "42", "sub-9", "c1", true); err != nil {
		t.Errorf("like: %v", err)
	}
}

// A source that keeps no comments must be told apart from an empty thread: the
// interface hides the section instead of showing "no comments yet".
func TestCommentsUnsupportedSource(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "d"), AppVersion: "1.6.3", Locale: "zh_CN"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx := context.Background()
	if _, err := c.Install(ctx, `class Quiet extends ComicSource {
  name = 'Quiet'; key = 'quiet'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
}`, "file", ""); err != nil {
		t.Fatal(err)
	}
	if c.CommentsSupported(ctx, "quiet") || c.CanSendComment(ctx, "quiet") {
		t.Fatal("a source without comments reported support")
	}
	if _, err := c.Comments(ctx, "quiet", "1", "", 1, ""); err == nil {
		t.Fatal("loading comments from a source without them succeeded")
	} else if !strings.Contains(err.Error(), "no comments") {
		t.Errorf("error = %v", err)
	}
	if err := c.SendComment(ctx, "quiet", "1", "", "hi", ""); err == nil {
		t.Fatal("sending to a source without sendComment succeeded")
	}
}

// The details carry the sub id, which a thread may be keyed by.
func TestDetailsCarrySubID(t *testing.T) {
	c := commentsCore(t)
	d, err := c.Details(context.Background(), "talk", "42")
	if err != nil {
		t.Fatal(err)
	}
	if d.SubID != "sub-9" {
		t.Errorf("subId = %q", d.SubID)
	}
	if raw, err := json.Marshal(d); err != nil || len(raw) == 0 {
		t.Errorf("details do not marshal: %v", err)
	}
}

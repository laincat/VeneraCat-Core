package mobile

import (
	"encoding/json"
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
        {id: 7, userName: 'ada', content: 'first ' + comicId + '/' + subId + '/p' + page + '/' + (replyTo ?? 'none'), time: '昨天', score: 3, isLiked: true, replyCount: '2'},
        {userName: 'bob', content: 'no id here'}
      ],
      maxPage: 4
    }),
    sendComment: async (comicId, subId, content, replyTo) => 'ok',
    likeComment: async () => 'ok',
  }
}`

func commentsCore(t *testing.T) *Core {
	t.Helper()
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", commentsSource, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCommentSupportIsReadFromTheComicObject(t *testing.T) {
	c := commentsCore(t)
	raw, err := c.CommentSupport("talk")
	if err != nil {
		t.Fatal(err)
	}
	var support struct{ Load, Send, Like bool }
	if err := json.Unmarshal([]byte(raw), &support); err != nil {
		t.Fatal(err)
	}
	if !support.Load || !support.Send || !support.Like {
		t.Fatalf("support = %s", raw)
	}
}

func TestComicComments(t *testing.T) {
	c := commentsCore(t)
	raw, err := c.ComicComments(0, "talk", "42", "sub-9", 2, "")
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	var page struct {
		Comments []struct {
			ID         string `json:"id"`
			UserName   string `json:"userName"`
			Content    string `json:"content"`
			Score      string `json:"score"`
			ReplyCount int    `json:"replyCount"`
			IsLiked    bool   `json:"isLiked"`
		} `json:"comments"`
		MaxPage int `json:"maxPage"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("comments are not JSON: %v (%s)", err, raw)
	}
	if page.MaxPage != 4 || len(page.Comments) != 2 {
		t.Fatalf("page = %s", raw)
	}
	first := page.Comments[0]
	// The official argument order, with null where the caller had nothing.
	if first.Content != "first 42/sub-9/p2/none" {
		t.Errorf("content = %q", first.Content)
	}
	// A numeric id and score stay readable text; a string count becomes a number.
	if first.ID != "7" || first.Score != "3" || first.ReplyCount != 2 || !first.IsLiked {
		t.Errorf("fields = %+v", first)
	}
	if page.Comments[1].UserName != "bob" {
		t.Errorf("a comment without an id was dropped: %+v", page.Comments[1])
	}
	if err := c.SendComicComment(0, "talk", "42", "sub-9", "hi", ""); err != nil {
		t.Errorf("send: %v", err)
	}
}

// A source without comments must be told apart from an empty thread.
func TestCommentSupportWithoutComments(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", `class Quiet extends ComicSource {
  name = 'Quiet'; key = 'quiet'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
}`, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := c.CommentSupport("quiet")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "true") {
		t.Errorf("a source without comments reported support: %s", raw)
	}
}

// Voting is the same call with the other flag, and a source without it is told
// apart rather than asked.
func TestLikeComicComment(t *testing.T) {
	c := commentsCore(t)
	if err := c.LikeComicComment(0, "talk", "42", "sub-9", "c1", true); err != nil {
		t.Errorf("like: %v", err)
	}
	if err := c.LikeComicComment(0, "talk", "42", "sub-9", "c1", false); err != nil {
		t.Errorf("unlike: %v", err)
	}
	raw, err := c.CommentSupport("talk")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"like":true`) {
		t.Errorf("support = %s", raw)
	}
}

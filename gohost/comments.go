package gohost

import (
	"context"
	"encoding/json"
	"fmt"
)

// Comments on a comic, when the source keeps them.
//
// Twenty of the official sources declare `loadComments` and thirteen declare
// `sendComment`; nothing in this client ever called either, so a source's
// comment thread was simply unreachable. The contract is the official one:
//
//	comic.loadComments(comicId, subId, page, replyTo) -> {comments: [...], maxPage}
//	comic.sendComment(comicId, subId, content, replyTo) -> any value on success
//	comic.likeComment(comicId, subId, commentId, isLike) -> any value on success
//
// They hang off the source's `comic` object, not off the source itself: looking
// for a top-level `loadComments` reports "keeps no comments" for every source
// that has them.
//
// subId is the source's secondary identifier for a comic (ComicDetails.subId),
// and a thread may be paged and may be read as replies to one comment — both are
// part of the call, not an afterthought.

// Comment is one comment as a source returned it. Fields a source leaves out
// stay empty rather than becoming zeros: a comment with no score is not a
// comment scored zero.
type Comment struct {
	ID       string
	UserName string
	Avatar   string
	Content  string
	Time     string
	// ReplyCount is how many replies the source reports, when it says.
	ReplyCount int
	// Score and IsLiked are the source's own numbers and flags; a source that
	// reports neither leaves them empty.
	Score   string
	IsLiked bool
}

// CommentsPage is one page of a thread.
type CommentsPage struct {
	Comments []Comment
	// MaxPage is 0 when the source does not say how many pages there are.
	MaxPage int
}

// CommentsSupported reports whether the source keeps comments at all.
func (c *Core) CommentsSupported(ctx context.Context, key string) bool {
	return c.Has(ctx, key, "comic.loadComments")
}

// CanSendComment reports whether the source accepts new comments.
func (c *Core) CanSendComment(ctx context.Context, key string) bool {
	return c.Has(ctx, key, "comic.sendComment")
}

// CanLikeComment reports whether the source votes on comments.
func (c *Core) CanLikeComment(ctx context.Context, key string) bool {
	return c.Has(ctx, key, "comic.likeComment")
}

// Comments loads one page of a comic's comments. replyTo is the comment whose
// replies to read, or "" for the thread itself.
func (c *Core) Comments(ctx context.Context, key, comicID, subID string, page int, replyTo string) (CommentsPage, error) {
	if !c.CommentsSupported(ctx, key) {
		return CommentsPage{}, fmt.Errorf("core: %s keeps no comments", key)
	}
	if page < 1 {
		page = 1
	}
	raw, err := c.Call(ctx, key, "comic.loadComments", comicID, nullIfEmpty(subID), page, nullIfEmpty(replyTo))
	if err != nil {
		return CommentsPage{}, err
	}
	var f struct {
		Comments json.RawMessage `json:"comments"`
		MaxPage  json.Number     `json:"maxPage"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return CommentsPage{}, fmt.Errorf("core: loadComments: %w", err)
	}
	out := CommentsPage{}
	if n, err := f.MaxPage.Int64(); err == nil {
		out.MaxPage = int(n)
	}
	var items []json.RawMessage
	if len(f.Comments) > 0 {
		// One malformed comment must not hide the rest of the thread.
		_ = json.Unmarshal(mustArray(f.Comments), &items)
	}
	for _, item := range items {
		var one struct {
			ID         flexString `json:"id"`
			UserName   flexString `json:"userName"`
			Avatar     flexString `json:"avatar"`
			Content    flexString `json:"content"`
			Time       flexString `json:"time"`
			ReplyCount flexString `json:"replyCount"`
			Score      flexString `json:"score"`
			IsLiked    bool       `json:"isLiked"`
		}
		if json.Unmarshal(item, &one) != nil {
			continue
		}
		out.Comments = append(out.Comments, Comment{
			ID: string(one.ID), UserName: string(one.UserName), Avatar: string(one.Avatar),
			Content: string(one.Content), Time: string(one.Time),
			ReplyCount: atoiOr(one.ReplyCount, 0),
			Score:      string(one.Score), IsLiked: one.IsLiked,
		})
	}
	return out, nil
}

// SendComment posts a comment and returns whatever the source answered, which
// the official API treats as "any value means success".
func (c *Core) SendComment(ctx context.Context, key, comicID, subID, content, replyTo string) error {
	if !c.CanSendComment(ctx, key) {
		return fmt.Errorf("core: %s does not accept comments", key)
	}
	_, err := c.Call(ctx, key, "comic.sendComment", comicID, nullIfEmpty(subID), content, nullIfEmpty(replyTo))
	return err
}

// LikeComment votes on a comment. isLike false takes the vote back.
func (c *Core) LikeComment(ctx context.Context, key, comicID, subID, commentID string, isLike bool) error {
	if !c.CanLikeComment(ctx, key) {
		return fmt.Errorf("core: %s does not vote on comments", key)
	}
	_, err := c.Call(ctx, key, "comic.likeComment", comicID, nullIfEmpty(subID), commentID, isLike)
	return err
}

// nullIfEmpty keeps a missing identifier out of the call: sources test for null
// and treat an empty string as a real, empty value.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// atoiOr reads a number a source may write as a string.
func atoiOr(v flexString, fallback int) int {
	if v == "" {
		return fallback
	}
	n := 0
	for _, r := range string(v) {
		if r < '0' || r > '9' {
			return fallback
		}
		n = n*10 + int(r-'0')
	}
	return n
}

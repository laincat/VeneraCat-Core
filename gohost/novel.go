package gohost

import (
	"context"

	"github.com/laincat/veneracat-core/novel"
)

// NovelPopular, NovelSearch, NovelDetail and NovelChapter delegate to the shared
// novel provider. Chapter sanitisation and rendering belong to the client, so
// Chapter answers with the raw HTML the source returned.
//
// A novel source reaches the network and its cookies through the same runtime
// and the same partition (comic:<key>) as the comic sources it shares an engine
// kind with, so nothing separate is assembled here.
func (c *Core) NovelPopular(ctx context.Context, key string, page int) (*novel.Page, error) {
	provider, err := c.reg.Novel(key)
	if err != nil {
		return nil, err
	}
	return provider.Popular(ctx, page)
}

func (c *Core) NovelSearch(ctx context.Context, key, keyword string, page int) (*novel.Page, error) {
	provider, err := c.reg.Novel(key)
	if err != nil {
		return nil, err
	}
	return provider.Search(ctx, keyword, page)
}

func (c *Core) NovelDetail(ctx context.Context, key, id string) (*novel.Detail, error) {
	provider, err := c.reg.Novel(key)
	if err != nil {
		return nil, err
	}
	return provider.Detail(ctx, id)
}

func (c *Core) NovelChapter(ctx context.Context, key, novelID, chapterID string) (*novel.ChapterContent, error) {
	provider, err := c.reg.Novel(key)
	if err != nil {
		return nil, err
	}
	return provider.Chapter(ctx, novelID, chapterID)
}

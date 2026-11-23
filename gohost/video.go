package gohost

import (
	"context"
	"net/http"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

// VideoSearch, VideoDetails and VideoPlay delegate to the shared provider. The
// desktop owns transport and playback; source parsing stays in VeneraCat Core.
func (c *Core) VideoSearch(ctx context.Context, key, keyword, cursor string) (*video.Page, error) {
	provider, err := c.reg.Video(key)
	if err != nil {
		return nil, err
	}
	return provider.Search(ctx, keyword, cursor)
}

func (c *Core) VideoDetails(ctx context.Context, key, id string) (*video.Details, error) {
	provider, err := c.reg.Video(key)
	if err != nil {
		return nil, err
	}
	return provider.Details(ctx, id)
}

func (c *Core) VideoPlay(ctx context.Context, key, itemID, lineID, episodeID string) (*video.Play, error) {
	provider, err := c.reg.Video(key)
	if err != nil {
		return nil, err
	}
	play, err := provider.Play(ctx, itemID, lineID, episodeID)
	if err != nil {
		return nil, err
	}
	if err := play.Validate(); err != nil {
		return nil, err
	}
	return play, nil
}

// OpenVideoMedia leaves media bytes streaming rather than buffering an entire
// movie. The caller closes the response and owns ctx. JS video sources use
// Core's existing source partition (comic:<key>) as their Network calls do;
// inventing a different media partition would silently lose their login state.
func (c *Core) OpenVideoMedia(ctx context.Context, key, rawURL string, headers http.Header) (*http.Response, error) {
	if _, err := c.reg.Video(key); err != nil {
		return nil, err
	}
	if err := host.ValidateURL(rawURL); err != nil {
		return nil, err
	}
	return c.tr.OpenStream(ctx, &host.Request{
		URL: rawURL, Header: headers, Cookies: true, Partition: ComicPartition(key),
	})
}

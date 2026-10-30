package plugin

import (
	"context"
	"fmt"

	"github.com/laincat/veneracat-core/video"
)

// checked wraps every video provider the registry hands out, so a Play
// result from any source (script, rule or built-in) passes Play.Validate:
// http(s) URLs only and well-formed headers.
type checked struct{ video.Provider }

func (c checked) Play(ctx context.Context, itemID, lineID, episodeID string) (*video.Play, error) {
	p, err := c.Provider.Play(ctx, itemID, lineID, episodeID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("plugin: %s returned no play result", c.Info().Key)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("plugin: %s returned an unsafe play result: %w", c.Info().Key, err)
	}
	return p, nil
}

// checkedExplorer keeps the Explorer interface of providers that have one.
type checkedExplorer struct {
	checked
	video.Explorer
}

func (c checkedExplorer) Info() video.Info { return c.checked.Info() }

func wrap(p video.Provider) video.Provider {
	if e, ok := p.(video.Explorer); ok {
		return checkedExplorer{checked{p}, e}
	}
	return checked{p}
}

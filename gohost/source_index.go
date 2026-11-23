package gohost

import (
	"context"

	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/subscription"
)

// InstallComicEntry keeps the index's declared identity authoritative. Core
// checks the downloaded script against that identity before publishing it.
func (c *Core) InstallComicEntry(ctx context.Context, entry subscription.Entry, body, indexURL string) (*plugin.Record, error) {
	return c.reg.InstallFromComicIndex(ctx, indexURL, entry, body)
}

func (c *Core) InstallExpecting(ctx context.Context, key string, typ plugin.Type, body, origin, indexURL, digest string) (*plugin.Record, error) {
	return c.reg.InstallExpecting(ctx, key, typ, body, origin, indexURL, digest)
}

func (c *Core) ReplaceExpecting(ctx context.Context, key string, typ plugin.Type, body, origin, indexURL, digest string) (*plugin.Record, error) {
	return c.reg.ReplaceExpecting(ctx, key, typ, body, origin, indexURL, digest)
}

func (c *Core) Record(key string) (plugin.Record, bool) { return c.reg.Get(key) }

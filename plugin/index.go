package plugin

import (
	"context"
	"fmt"

	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/subscription"
)

// FromVideoIndex maps a vcvideo.json plugin type to a registry type.
func FromVideoIndex(t string) (Type, error) {
	switch t {
	case subscription.TypeJS:
		return TypeVideoJS, nil
	case subscription.TypeKazumi:
		return TypeKazumi, nil
	}
	return "", fmt.Errorf("plugin: vcvideo type %q is not supported", t)
}

// InstallFromVideoIndex installs an entry of a vcvideo.json index. body is
// the file the client downloaded from entry.URL; the entry's sha256, when
// present, is enforced. The installed key must equal the entry's key, so an
// index cannot install something other than what it lists.
func (r *Registry) InstallFromVideoIndex(ctx context.Context, indexURL string, entry subscription.Plugin, body string) (*Record, error) {
	typ, err := FromVideoIndex(entry.Type)
	if err != nil {
		return nil, err
	}
	return r.installExpecting(ctx, entry.Key, typ, body, entry.URL, indexURL, entry.SHA256, entry.Attribution)
}

// InstallFromComicIndex installs an entry of a comic index.json. body is the
// downloaded script. The script's key must equal the entry's key.
func (r *Registry) InstallFromComicIndex(ctx context.Context, indexURL string, entry subscription.Entry, body string) (*Record, error) {
	return r.installExpecting(ctx, entry.Key, TypeComic, body, entry.URL, indexURL, "", entry.Attribution)
}

// installExpecting installs body only if it declares wantKey. The check and
// the install happen under the operation lock, so a mismatching plugin is
// never visible in the registry.
func (r *Registry) installExpecting(ctx context.Context, wantKey string, typ Type, body, origin, indexURL, sha string, indexAttr attribution.Info) (*Record, error) {
	r.op.Lock()
	defer r.op.Unlock()
	sum := digest(body)
	if sha != "" && sha != sum {
		return nil, ErrChecksum
	}
	if r.taken(wantKey) {
		return nil, fmt.Errorf("%w: %s", ErrExists, wantKey)
	}
	a, err := r.activate(ctx, typ, body)
	if err != nil {
		return nil, err
	}
	if a.key != wantKey {
		// activate succeeded, so the runtime held no script under a.key
		// before: the loaded engine is ours to unload.
		r.deactivate(ctx, a)
		return nil, fmt.Errorf("%w: index lists %s, script declares %s", ErrKey, wantKey, a.key)
	}
	if r.taken(a.key) {
		r.deactivate(ctx, a)
		return nil, fmt.Errorf("%w: %s", ErrExists, a.key)
	}
	rec := &Record{Type: typ, Key: a.key, Body: body, SHA256: sum, Origin: origin, IndexURL: indexURL,
		Enabled: true, IndexAttribution: indexAttr}
	a.apply(rec)
	r.publish(rec, a.provider)
	return clone(rec), nil
}

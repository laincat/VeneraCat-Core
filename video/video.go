// Package video defines the data model shared by every video provider:
// JavaScript VideoSource plugins, imported Kazumi rules and the built-in
// providers rewritten from other apps.
package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/host"
)

// Item is a search or catalogue result.
type Item struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Cover    string   `json:"cover,omitempty"`
	Subtitle string   `json:"subtitle,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Extra keeps provider-specific fields the UI may show (episode count,
	// status). Keys are stable per provider.
	Extra map[string]string `json:"extra,omitempty"`
}

// Page is one page of results. Next is an opaque cursor; empty means the
// last page.
type Page struct {
	Items []Item `json:"items"`
	Next  string `json:"next,omitempty"`
}

// Episode is one playable unit inside a line.
type Episode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Line is a playback route ("线路"). Sites often mirror the same episodes on
// several CDNs or players.
type Line struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Episodes []Episode `json:"episodes"`
}

// Details is the detail page of an Item.
type Details struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Cover       string   `json:"cover,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Lines       []Line   `json:"lines"`
}

// Format of a media URL.
type Format string

const (
	FormatAuto Format = "auto"
	FormatHLS  Format = "hls"
	FormatMP4  Format = "mp4"
	FormatDASH Format = "dash"
)

// HLSOptions asks the client's local HLS proxy to rewrite playlists. Core's
// media package implements the rewriting; the client only proxies bytes.
type HLSOptions struct {
	FilterAds bool `json:"filterAds,omitempty"`
}

// Sniff asks the client to find the media URL with a WebView.
type Sniff struct {
	Page          string `json:"page"`
	Match         string `json:"match,omitempty"`
	FollowIframes bool   `json:"followIframes,omitempty"`
	TimeoutMillis int    `json:"timeoutMs,omitempty"`
}

// Variant is one quality of the same episode.
type Variant struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Quality int    `json:"quality,omitempty"`
}

// Play is how to play an episode. Exactly one of URL or Sniff is set.
type Play struct {
	URL      string      `json:"url,omitempty"`
	Format   Format      `json:"format,omitempty"`
	Header   http.Header `json:"headers,omitempty"`
	Variants []Variant   `json:"variants,omitempty"`
	HLS      *HLSOptions `json:"hls,omitempty"`
	Sniff    *Sniff      `json:"sniff,omitempty"`
	// Encrypted marks media the client cannot play directly (for example
	// common-encryption MP4 without a key path). The client must show an
	// error instead of a black screen.
	Encrypted bool `json:"encrypted,omitempty"`
}

// Validate checks the Play invariants and that every URL is http(s) and
// every header is well formed, so a plugin cannot point the player at a
// local file or inject header lines.
func (p *Play) Validate() error {
	switch {
	case p.URL == "" && p.Sniff == nil:
		return errors.New("video: play has neither url nor sniff")
	case p.URL != "" && p.Sniff != nil:
		return errors.New("video: play has both url and sniff")
	case p.Sniff != nil && p.Sniff.Page == "":
		return errors.New("video: sniff page is empty")
	}
	check := func(what, u string) error {
		if err := httpURL(u); err != nil {
			return fmt.Errorf("video: %s: %w", what, err)
		}
		return nil
	}
	if p.URL != "" {
		if err := check("url", p.URL); err != nil {
			return err
		}
	}
	if p.Sniff != nil {
		if err := check("sniff page", p.Sniff.Page); err != nil {
			return err
		}
		if p.Sniff.Match != "" {
			if _, err := regexp.Compile(p.Sniff.Match); err != nil {
				return fmt.Errorf("video: sniff match: %w", err)
			}
		}
	}
	for i, v := range p.Variants {
		if err := check(fmt.Sprintf("variant %d", i), v.URL); err != nil {
			return err
		}
	}
	for name, vals := range p.Header {
		for _, v := range vals {
			if err := host.ValidHeader(name, v); err != nil {
				return fmt.Errorf("video: %w", err)
			}
		}
	}
	return nil
}

func httpURL(raw string) error { return host.ValidateURL(raw) }

// Provider is the interface every video source implements.
type Provider interface {
	Info() Info
	Search(ctx context.Context, keyword string, cursor string) (*Page, error)
	Details(ctx context.Context, id string) (*Details, error)
	Play(ctx context.Context, itemID, lineID, episodeID string) (*Play, error)
}

// Explorer is implemented by providers that have a browsable catalogue.
type Explorer interface {
	Categories() []Category
	Explore(ctx context.Context, categoryID string, cursor string) (*Page, error)
}

// Category is a browsable catalogue section.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Info identifies a provider.
type Info struct {
	// Key matches ^[a-zA-Z0-9_]+$, the same rule as comic sources.
	Key     string `json:"key"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Kind is "js", "kazumi-rule" or "builtin".
	Kind    string `json:"kind"`
	BaseURL string `json:"baseUrl,omitempty"`
	// Attribution is who wrote the provider and where it is published.
	Attribution attribution.Info `json:"attribution"`
}

// ErrNotFound means the site has no such item or episode.
var ErrNotFound = errors.New("video: not found")

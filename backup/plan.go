package backup

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Selection chooses what to restore. Cookies are opt-in because they carry
// logged-in sessions.
type Selection struct {
	History        bool
	Favorites      bool
	ImageFavorites bool
	Settings       bool
	SearchHistory  bool
	Sources        bool
	SourceData     bool
	Cookies        bool
	// Folders limits favourites to these folder names; nil means all.
	Folders []string
}

// DefaultSelection restores everything except cookies.
func DefaultSelection() Selection {
	return Selection{History: true, Favorites: true, ImageFavorites: true, Settings: true,
		SearchHistory: true, Sources: true, SourceData: true}
}

// Plan is what the client should apply. Rows whose source could not be
// resolved are in Pending, not in History or Folders: the client keeps them
// and retries once the user installs the matching source.
type Plan struct {
	History        []HistoryItem              `json:"history"`
	Folders        []Folder                   `json:"folders"`
	ImageFavorites []ImageFavorite            `json:"imageFavorites"`
	Settings       map[string]json.RawMessage `json:"settings,omitempty"`
	SearchHistory  []string                   `json:"searchHistory,omitempty"`
	// Sources are installed through the client's normal install flow, which
	// validates and previews them. Restore never runs them.
	Sources []SourceRestore `json:"sources"`
	Cookies []Cookie        `json:"cookies,omitempty"`
	// CookieTargets names the partitions the client should store Cookies in. It is
	// filled by the caller that knows which sources are installed; restoring never
	// recreates the old single jar.
	CookieTargets []string `json:"cookieTargets,omitempty"`
	Pending       Pending  `json:"pending"`
}

// SourceRestore is a source to reinstall.
type SourceRestore struct {
	Key    string          `json:"key"`
	Script string          `json:"script,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Pending holds rows that need a source the client does not know yet.
type Pending struct {
	History   []HistoryItem             `json:"history,omitempty"`
	Favorites map[string][]FavoriteItem `json:"favorites,omitempty"`
}

// Summary counts what a Plan contains, for the confirmation screen.
type Summary struct {
	History, Folders, FavoriteItems, ImageFavorites, Settings, SearchHistory, Sources, Cookies int
	PendingHistory, PendingFavorites                                                           int
}

// Plan builds the restore plan for a selection.
func (c *Content) Plan(sel Selection) (*Plan, error) {
	p := &Plan{History: []HistoryItem{}, Folders: []Folder{}, ImageFavorites: []ImageFavorite{}, Sources: []SourceRestore{}}
	if sel.History {
		for _, h := range c.History {
			if h.SourceKey == "" {
				p.Pending.History = append(p.Pending.History, h)
				continue
			}
			p.History = append(p.History, h)
		}
	}
	if sel.Favorites {
		var only map[string]bool
		if sel.Folders != nil {
			only = map[string]bool{}
			for _, n := range sel.Folders {
				only[n] = true
			}
			known := map[string]bool{}
			for _, f := range c.Folders {
				known[f.Name] = true
			}
			for _, n := range sel.Folders {
				if !known[n] {
					return nil, fmt.Errorf("backup: folder %q is not in the backup", n)
				}
			}
		}
		for _, f := range c.Folders {
			if only != nil && !only[f.Name] {
				continue
			}
			out := Folder{Name: f.Name, Order: f.Order, Sync: f.Sync, Items: []FavoriteItem{}}
			for _, it := range f.Items {
				if it.SourceKey == "" {
					if p.Pending.Favorites == nil {
						p.Pending.Favorites = map[string][]FavoriteItem{}
					}
					p.Pending.Favorites[f.Name] = append(p.Pending.Favorites[f.Name], it)
					continue
				}
				out.Items = append(out.Items, it)
			}
			p.Folders = append(p.Folders, out)
		}
	}
	if sel.ImageFavorites {
		p.ImageFavorites = append(p.ImageFavorites, c.ImageFavorites...)
	}
	if sel.Settings && len(c.Settings) > 0 {
		p.Settings = make(map[string]json.RawMessage, len(c.Settings))
		for k, v := range c.Settings {
			p.Settings[k] = v
		}
	}
	if sel.SearchHistory {
		p.SearchHistory = append(p.SearchHistory, c.SearchHistory...)
	}
	if sel.Sources || sel.SourceData {
		for _, s := range c.Sources {
			r := SourceRestore{Key: s.Key}
			if sel.Sources && s.HasScript {
				r.Script = s.Script
			}
			if sel.SourceData && s.HasData {
				r.Data = s.Data
			}
			if r.Script != "" || r.Data != nil {
				p.Sources = append(p.Sources, r)
			}
		}
		sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].Key < p.Sources[j].Key })
	}
	if sel.Cookies {
		p.Cookies = append(p.Cookies, c.Cookies...)
	}
	return p, nil
}

// Summary counts a plan.
func (p *Plan) Summary() Summary {
	s := Summary{History: len(p.History), Folders: len(p.Folders), ImageFavorites: len(p.ImageFavorites),
		Settings: len(p.Settings), SearchHistory: len(p.SearchHistory), Sources: len(p.Sources), Cookies: len(p.Cookies),
		PendingHistory: len(p.Pending.History)}
	for _, f := range p.Folders {
		s.FavoriteItems += len(f.Items)
	}
	for _, items := range p.Pending.Favorites {
		s.PendingFavorites += len(items)
	}
	return s
}

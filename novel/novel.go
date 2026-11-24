// Package novel defines the data model shared by novel providers.
package novel

import (
	"context"
	"errors"

	"github.com/laincat/veneracat-core/attribution"
)

// CapabilityJS is the shared source collection capability name for novel-js plugins.
const CapabilityJS = "novel-source.js"

// Item is a catalogue or search result.
type Item struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Cover  string `json:"cover,omitempty"`
	Author string `json:"author,omitempty"`
}

// Page is one page of novel items.
type Page struct {
	Items   []Item `json:"items"`
	MaxPage int    `json:"maxPage,omitempty"`
}

// Chapter is one entry in a novel's table of contents.
type Chapter struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Time  string `json:"time,omitempty"`
}

// Detail is the metadata and chapter list of a novel.
type Detail struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Cover    string    `json:"cover,omitempty"`
	Author   string    `json:"author,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	Status   string    `json:"status,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Chapters []Chapter `json:"chapters"`
}

// ChapterContent holds the raw chapter HTML returned by a provider.
// Sanitisation and rendering belong to the client layer.
type ChapterContent struct {
	HTML string `json:"html"`
}

// Info identifies a novel provider.
type Info struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Kind        string           `json:"kind"`
	BaseURL     string           `json:"baseUrl,omitempty"`
	Attribution attribution.Info `json:"attribution"`
}

// Provider is the interface every novel source implements.
type Provider interface {
	Info() Info
	Popular(ctx context.Context, page int) (*Page, error)
	Search(ctx context.Context, keyword string, page int) (*Page, error)
	Detail(ctx context.Context, id string) (*Detail, error)
	Chapter(ctx context.Context, novelID, chapterID string) (*ChapterContent, error)
}

// ErrNotFound means the site has no such novel or chapter.
var ErrNotFound = errors.New("novel: not found")

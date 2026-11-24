// Package adapt turns novel chapter HTML into structured comic storyboards and
// persistent character sheets using an abstract TextGenerator.
//
// Core owns only data models, HTML-to-text chunking, prompt construction,
// strict JSON validation, cross-chapter character merging, and budget
// enforcement. Network transport, credentials, and routing remain in the host
// or external plugins.
package adapt

import (
	"errors"
	"fmt"
)

// ImageStatus tracks the rendering state of one storyboard panel.
type ImageStatus string

const (
	ImagePending    ImageStatus = "pending"
	ImageGenerating ImageStatus = "generating"
	ImageDone       ImageStatus = "done"
	ImageFailed     ImageStatus = "failed"
)

// StyleRef points to a local comic used as a personal style reference.
type StyleRef struct {
	ComicSourceKey   string   `json:"comicSourceKey,omitempty"`
	ComicID          string   `json:"comicId,omitempty"`
	ComicTitle       string   `json:"comicTitle,omitempty"`
	ReferencePageIDs []string `json:"referencePageIds,omitempty"`
	StyleNotes       string   `json:"styleNotes,omitempty"`
}

// Character records a character's visual and narrative identity across chapters.
type Character struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Aliases        []string `json:"aliases,omitempty"`
	Appearance     string   `json:"appearance,omitempty"`
	Outfit         string   `json:"outfit,omitempty"`
	Traits         []string `json:"traits,omitempty"`
	FirstChapterID string   `json:"firstChapterId,omitempty"`
	Notes          string   `json:"notes,omitempty"`
}

// CharacterSheet holds the canonical character roster of a Project.
type CharacterSheet struct {
	Characters []Character `json:"characters"`
}

// Dialogue is one speech, thought, narration, or sound-effect line in a panel.
type Dialogue struct {
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text"`
	Kind    string `json:"kind,omitempty"`
}

// Panel is a single comic frame inside a Scene.
type Panel struct {
	ID          string      `json:"id"`
	Shot        string      `json:"shot"`
	Action      string      `json:"action"`
	Characters  []string    `json:"characters,omitempty"`
	Dialogues   []Dialogue  `json:"dialogues,omitempty"`
	Prompt      string      `json:"prompt"`
	ImageStatus ImageStatus `json:"imageStatus"`
}

// Scene groups consecutive panels sharing a location and mood.
type Scene struct {
	ID      string  `json:"id"`
	Setting string  `json:"setting"`
	Mood    string  `json:"mood,omitempty"`
	Panels  []Panel `json:"panels"`
}

// Storyboard is the adapted panel script of one novel chapter.
type Storyboard struct {
	ChapterID    string  `json:"chapterId"`
	ChapterTitle string  `json:"chapterTitle,omitempty"`
	Scenes       []Scene `json:"scenes"`
}

// Budget caps adaptation spending. Zero fields mean unlimited.
type Budget struct {
	MaxCalls        int64 `json:"maxCalls,omitempty"`
	MaxInputTokens  int64 `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
	MaxTotalTokens  int64 `json:"maxTotalTokens,omitempty"`
}

// Usage tracks cumulative generator calls and token counts.
type Usage struct {
	Calls        int64 `json:"calls"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

// Add folds delta into u, computing TotalTokens when delta omitted it.
func (u *Usage) Add(delta Usage) {
	u.Calls += delta.Calls
	u.InputTokens += delta.InputTokens
	u.OutputTokens += delta.OutputTokens
	total := delta.TotalTokens
	if total == 0 && (delta.InputTokens > 0 || delta.OutputTokens > 0) {
		total = delta.InputTokens + delta.OutputTokens
	}
	u.TotalTokens += total
}

// CheckNextCall verifies whether another generator call is permitted under b.
func (u Usage) CheckNextCall(b Budget) error {
	if b.MaxCalls > 0 && u.Calls >= b.MaxCalls {
		return fmt.Errorf("%w: calls %d reached limit %d", ErrBudgetExceeded, u.Calls, b.MaxCalls)
	}
	if b.MaxInputTokens > 0 && u.InputTokens >= b.MaxInputTokens {
		return fmt.Errorf("%w: input tokens %d reached limit %d", ErrBudgetExceeded, u.InputTokens, b.MaxInputTokens)
	}
	if b.MaxOutputTokens > 0 && u.OutputTokens >= b.MaxOutputTokens {
		return fmt.Errorf("%w: output tokens %d reached limit %d", ErrBudgetExceeded, u.OutputTokens, b.MaxOutputTokens)
	}
	if b.MaxTotalTokens > 0 && u.TotalTokens >= b.MaxTotalTokens {
		return fmt.Errorf("%w: total tokens %d reached limit %d", ErrBudgetExceeded, u.TotalTokens, b.MaxTotalTokens)
	}
	return nil
}

// Project is the serialisable adaptation workspace for one novel.
type Project struct {
	ID             string                 `json:"id"`
	NovelSourceKey string                 `json:"novelSourceKey,omitempty"`
	NovelID        string                 `json:"novelId,omitempty"`
	Title          string                 `json:"title"`
	StyleRef       *StyleRef              `json:"styleRef,omitempty"`
	Characters     CharacterSheet         `json:"characters"`
	Storyboards    map[string]*Storyboard `json:"storyboards,omitempty"`
	Budget         Budget                 `json:"budget,omitempty"`
	Usage          Usage                  `json:"usage"`
}

// Sentinel errors returned by the adaptation pipeline.
var (
	ErrBudgetExceeded    = errors.New("adapt: budget exceeded")
	ErrInvalidStoryboard = errors.New("adapt: invalid storyboard JSON")
	ErrEmptyChapter      = errors.New("adapt: chapter text is empty")
)

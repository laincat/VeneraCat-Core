package gohost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

// AdaptTextConfig configures the standard AI text model backend used for
// novel-to-storyboard adaptation. All requests flow through Core's
// host.Transport with Cookies disabled; Core never reads credentials from
// environment variables.
type AdaptTextConfig struct {
	Protocol         aiapi.Protocol `json:"protocol"`
	BaseURL          string         `json:"baseUrl,omitempty"`
	APIKey           string         `json:"apiKey"`
	AnthropicVersion string         `json:"anthropicVersion,omitempty"`
	Partition        string         `json:"partition,omitempty"`
	Model            string         `json:"model"`
	MaxTokens        int            `json:"maxTokens,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	JSONMode         bool           `json:"jsonMode,omitempty"`
	ReasoningEffort  string         `json:"reasoningEffort,omitempty"`
}

// AdaptImageProvider identifies a built-in static comic image backend.
type AdaptImageProvider string

const (
	AdaptImageGemini AdaptImageProvider = "gemini"
	AdaptImageOpenAI AdaptImageProvider = "openai"
)

// AdaptImageConfig configures the static comic image backend used for panel
// rendering over Core's host.Transport with Cookies disabled.
type AdaptImageConfig struct {
	Provider           AdaptImageProvider `json:"provider"`
	BaseURL            string             `json:"baseUrl,omitempty"`
	APIKey             string             `json:"apiKey"`
	Model              string             `json:"model"`
	Partition          string             `json:"partition,omitempty"`
	ResponseModalities []string           `json:"responseModalities,omitempty"`
	Size               string             `json:"size,omitempty"`
	Quality            string             `json:"quality,omitempty"`
	OutputFormat       string             `json:"outputFormat,omitempty"`
	ResponseFormat     string             `json:"responseFormat,omitempty"`
}

// AdaptChapterRequest configures one chapter storyboard adaptation run.
// Supplying Options.Checkpoint resumes an interrupted multi-chunk chapter
// without re-billing completed chunks.
type AdaptChapterRequest struct {
	Project   *adapt.Project       `json:"project"`
	Chapter   novel.Chapter        `json:"chapter"`
	Content   novel.ChapterContent `json:"content"`
	Text      AdaptTextConfig      `json:"text"`
	Options   adapt.AdaptOptions   `json:"options,omitempty"`
	Generator adapt.TextGenerator  `json:"-"`
}

// RenderPanelRequest configures rendering or retrying a single storyboard
// panel. Already-done panels are skipped unless Options.ForceRerender is true.
type RenderPanelRequest struct {
	Project   *adapt.Project       `json:"project"`
	ChapterID string               `json:"chapterId"`
	SceneID   string               `json:"sceneId,omitempty"`
	PanelID   string               `json:"panelId"`
	Image     AdaptImageConfig     `json:"image"`
	Options   adapt.RenderOptions  `json:"options,omitempty"`
	Sink      adapt.ImageSink      `json:"-"`
	Generator adapt.ImageGenerator `json:"-"`
}

// RenderStoryboardRequest configures rendering all panels of one chapter
// storyboard in order. Already-done panels are skipped unless
// Options.ForceRerender is true.
type RenderStoryboardRequest struct {
	Project   *adapt.Project       `json:"project"`
	ChapterID string               `json:"chapterId"`
	Image     AdaptImageConfig     `json:"image"`
	Options   adapt.RenderOptions  `json:"options,omitempty"`
	Sink      adapt.ImageSink      `json:"-"`
	Generator adapt.ImageGenerator `json:"-"`
}

// AdaptChapter converts one novel chapter into a structured comic storyboard,
// updating req.Project.Characters and req.Project.Usage and enforcing
// req.Project.Budget before every model call. On partial failure (for example
// adapt.ErrBudgetExceeded, context cancellation, or a transient upstream error)
// it returns the partial AdaptResult carrying a serialisable Checkpoint
// alongside the error.
func (c *Core) AdaptChapter(ctx context.Context, req AdaptChapterRequest) (*adapt.AdaptResult, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	if req.Project == nil {
		return nil, errors.New("gohost: adapt project is required")
	}
	gen := req.Generator
	if gen == nil {
		built, err := c.newAdaptTextGenerator(req.Text)
		if err != nil {
			return nil, err
		}
		gen = built
	}
	return adapt.AdaptChapter(ctx, req.Project, req.Chapter, req.Content, gen, req.Options)
}

// RenderPanel renders a single storyboard panel via the configured image
// backend over Core's host.Transport. Local style and character reference
// images are passed through req.Options, rendered bytes are delivered to
// req.Sink, and req.Project.Budget is checked before every call.
func (c *Core) RenderPanel(ctx context.Context, req RenderPanelRequest) (*adapt.PanelRenderResult, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	if req.Project == nil {
		return nil, errors.New("gohost: adapt project is required")
	}
	if strings.TrimSpace(req.ChapterID) == "" {
		return nil, errors.New("gohost: adapt chapterId is required")
	}
	if strings.TrimSpace(req.PanelID) == "" {
		return nil, errors.New("gohost: adapt panelId is required")
	}
	gen := req.Generator
	if gen == nil {
		built, err := c.newAdaptImageGenerator(req.Image)
		if err != nil {
			return nil, err
		}
		gen = built
	}
	return adapt.RenderPanel(ctx, req.Project, req.ChapterID, req.SceneID, req.PanelID, gen, req.Sink, req.Options)
}

// RenderStoryboard renders every pending panel of req.Project.Storyboards[req.ChapterID].
// Panels already marked ImageDone are skipped unless req.Options.ForceRerender
// is true, allowing clients to resume after budget pauses or interruptions.
func (c *Core) RenderStoryboard(ctx context.Context, req RenderStoryboardRequest) (*adapt.RenderResult, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	if req.Project == nil {
		return nil, errors.New("gohost: adapt project is required")
	}
	if strings.TrimSpace(req.ChapterID) == "" {
		return nil, errors.New("gohost: adapt chapterId is required")
	}
	gen := req.Generator
	if gen == nil {
		built, err := c.newAdaptImageGenerator(req.Image)
		if err != nil {
			return nil, err
		}
		gen = built
	}
	return adapt.RenderStoryboard(ctx, req.Project, req.ChapterID, gen, req.Sink, req.Options)
}

func (c *Core) newAdaptTextGenerator(cfg AdaptTextConfig) (*adapt.AIAPIGenerator, error) {
	proto := aiapi.Protocol(strings.ToLower(strings.TrimSpace(string(cfg.Protocol))))
	switch proto {
	case aiapi.Chat, aiapi.Responses, aiapi.Messages:
	default:
		return nil, fmt.Errorf("gohost: unsupported adapt text protocol %q", cfg.Protocol)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("gohost: adapt text apiKey is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("gohost: adapt text model is required")
	}
	return &adapt.AIAPIGenerator{
		Config: aiapi.Config{
			Protocol:         proto,
			BaseURL:          strings.TrimSpace(cfg.BaseURL),
			APIKey:           strings.TrimSpace(cfg.APIKey),
			AnthropicVersion: strings.TrimSpace(cfg.AnthropicVersion),
			Partition:        strings.TrimSpace(cfg.Partition),
		},
		Model:           strings.TrimSpace(cfg.Model),
		MaxTokens:       cfg.MaxTokens,
		Temperature:     cfg.Temperature,
		JSONMode:        cfg.JSONMode,
		ReasoningEffort: strings.TrimSpace(cfg.ReasoningEffort),
		Transport:       c.tr,
	}, nil
}

func (c *Core) newAdaptImageGenerator(cfg AdaptImageConfig) (adapt.ImageGenerator, error) {
	provider := AdaptImageProvider(strings.ToLower(strings.TrimSpace(string(cfg.Provider))))
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("gohost: adapt image apiKey is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("gohost: adapt image model is required")
	}
	switch provider {
	case AdaptImageGemini:
		return &adapt.GeminiImageGenerator{
			BaseURL:            strings.TrimSpace(cfg.BaseURL),
			APIKey:             strings.TrimSpace(cfg.APIKey),
			Model:              strings.TrimSpace(cfg.Model),
			ResponseModalities: append([]string(nil), cfg.ResponseModalities...),
			Partition:          strings.TrimSpace(cfg.Partition),
			Transport:          c.tr,
		}, nil
	case AdaptImageOpenAI:
		return &adapt.OpenAIImageGenerator{
			BaseURL:        strings.TrimSpace(cfg.BaseURL),
			APIKey:         strings.TrimSpace(cfg.APIKey),
			Model:          strings.TrimSpace(cfg.Model),
			Size:           strings.TrimSpace(cfg.Size),
			Quality:        strings.TrimSpace(cfg.Quality),
			OutputFormat:   strings.TrimSpace(cfg.OutputFormat),
			ResponseFormat: strings.TrimSpace(cfg.ResponseFormat),
			Partition:      strings.TrimSpace(cfg.Partition),
			Transport:      c.tr,
		}, nil
	default:
		return nil, fmt.Errorf("gohost: unsupported adapt image provider %q", cfg.Provider)
	}
}

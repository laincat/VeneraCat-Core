package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

type adaptTextConfigWire struct {
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

type adaptImageConfigWire struct {
	Provider           string   `json:"provider"`
	BaseURL            string   `json:"baseUrl,omitempty"`
	APIKey             string   `json:"apiKey"`
	Model              string   `json:"model"`
	Partition          string   `json:"partition,omitempty"`
	ResponseModalities []string `json:"responseModalities,omitempty"`
	Size               string   `json:"size,omitempty"`
	Quality            string   `json:"quality,omitempty"`
	OutputFormat       string   `json:"outputFormat,omitempty"`
	ResponseFormat     string   `json:"responseFormat,omitempty"`
}

type adaptImageRefWire struct {
	ID         string             `json:"id,omitempty"`
	Kind       adapt.ImageRefKind `json:"kind,omitempty"`
	Label      string             `json:"label,omitempty"`
	MIMEType   string             `json:"mimeType,omitempty"`
	DataBase64 string             `json:"dataBase64,omitempty"`
	Data       string             `json:"data,omitempty"`
}

type adaptRenderOptionsWire struct {
	AspectRatio           string                         `json:"aspectRatio,omitempty"`
	Size                  string                         `json:"size,omitempty"`
	MaxRetries            int                            `json:"maxRetries,omitempty"`
	ForceRerender         bool                           `json:"forceRerender,omitempty"`
	ContinueOnError       bool                           `json:"continueOnError,omitempty"`
	StyleReferences       []adaptImageRefWire            `json:"styleReferences,omitempty"`
	CharacterReferences   map[string][]adaptImageRefWire `json:"characterReferences,omitempty"`
	FeedbackCharacterRefs bool                           `json:"feedbackCharacterRefs,omitempty"`
}

type adaptChapterRequestWire struct {
	Project *adapt.Project       `json:"project"`
	Chapter novel.Chapter        `json:"chapter"`
	Content novel.ChapterContent `json:"content"`
	Text    adaptTextConfigWire  `json:"text"`
	Options adapt.AdaptOptions   `json:"options,omitempty"`
}

type adaptChapterResponseWire struct {
	Completed bool               `json:"completed"`
	Project   *adapt.Project     `json:"project"`
	Result    *adapt.AdaptResult `json:"result,omitempty"`
	Error     string             `json:"error,omitempty"`
	ErrorCode string             `json:"errorCode,omitempty"`
}

type renderPanelRequestWire struct {
	Project   *adapt.Project         `json:"project"`
	ChapterID string                 `json:"chapterId"`
	SceneID   string                 `json:"sceneId,omitempty"`
	PanelID   string                 `json:"panelId"`
	Image     adaptImageConfigWire   `json:"image"`
	Options   adaptRenderOptionsWire `json:"options,omitempty"`
}

type renderPanelResponseWire struct {
	Project       *adapt.Project `json:"project"`
	SceneID       string         `json:"sceneId"`
	PanelID       string         `json:"panelId"`
	RefID         string         `json:"refId,omitempty"`
	Skipped       bool           `json:"skipped,omitempty"`
	MIMEType      string         `json:"mimeType,omitempty"`
	DataBase64    string         `json:"dataBase64,omitempty"`
	RevisedPrompt string         `json:"revisedPrompt,omitempty"`
	Usage         adapt.Usage    `json:"usage"`
}

type renderStoryboardRequestWire struct {
	Project   *adapt.Project         `json:"project"`
	ChapterID string                 `json:"chapterId"`
	Image     adaptImageConfigWire   `json:"image"`
	Options   adaptRenderOptionsWire `json:"options,omitempty"`
}

type renderedPanelImageWire struct {
	SceneID    string `json:"sceneId"`
	PanelID    string `json:"panelId"`
	RefID      string `json:"refId"`
	MIMEType   string `json:"mimeType"`
	DataBase64 string `json:"dataBase64"`
}

type panelFailureWire struct {
	SceneID   string `json:"sceneId"`
	PanelID   string `json:"panelId"`
	Error     string `json:"error"`
	ErrorCode string `json:"errorCode"`
}

type renderSummaryWire struct {
	ChapterID string             `json:"chapterId"`
	Rendered  int                `json:"rendered"`
	Skipped   int                `json:"skipped"`
	Failed    int                `json:"failed"`
	Blocked   int                `json:"blocked"`
	Usage     adapt.Usage        `json:"usage"`
	Failures  []panelFailureWire `json:"failures,omitempty"`
}

type renderStoryboardResponseWire struct {
	Completed bool                     `json:"completed"`
	Project   *adapt.Project           `json:"project"`
	Result    renderSummaryWire        `json:"result"`
	Images    []renderedPanelImageWire `json:"images,omitempty"`
	Error     string                   `json:"error,omitempty"`
	ErrorCode string                   `json:"errorCode,omitempty"`
}

// AdaptChapter converts one novel chapter into a structured comic storyboard
// over Core's host bridge (Cookies: false). It supports resumable multi-chunk
// execution via options.checkpoint: when a chapter stops partway after at least
// one chunk completed (for example due to budget exhaustion, cancellation, or a
// transient upstream failure), the JSON response carries completed=false,
// error/errorCode, the updated project, and result.checkpoint so the client can
// persist progress and resume without re-billing completed chunks.
func (c *Core) AdaptChapter(callID int64, requestJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)

	var req adaptChapterRequestWire
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("mobile: invalid adapt chapter request JSON: %w", err)
	}
	if req.Project == nil {
		return "", errors.New("mobile: adapt project is required")
	}
	gen, err := c.newAdaptTextGenerator(req.Text)
	if err != nil {
		return "", err
	}

	ctx, done := c.ctx(callID)
	defer done()

	res, adaptErr := adapt.AdaptChapter(ctx, req.Project, req.Chapter, req.Content, gen, req.Options)
	if adaptErr != nil {
		if res != nil && res.Checkpoint != nil && res.CompletedChunks > 0 {
			return encode(adaptChapterResponseWire{
				Completed: false,
				Project:   req.Project,
				Result:    res,
				Error:     adaptErr.Error(),
				ErrorCode: adaptErrorCode(adaptErr),
			})
		}
		return "", adaptErr
	}

	return encode(adaptChapterResponseWire{
		Completed: true,
		Project:   req.Project,
		Result:    res,
	})
}

// RenderPanel renders or retries a single static comic panel over Core's host
// bridge (Cookies: false). Already-done panels are skipped unless
// options.forceRerender is true. Rendered image bytes are returned as base64 in
// the JSON response together with the updated project state.
func (c *Core) RenderPanel(callID int64, requestJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)

	var req renderPanelRequestWire
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("mobile: invalid render panel request JSON: %w", err)
	}
	if req.Project == nil {
		return "", errors.New("mobile: adapt project is required")
	}
	if strings.TrimSpace(req.ChapterID) == "" {
		return "", errors.New("mobile: adapt chapterId is required")
	}
	if strings.TrimSpace(req.PanelID) == "" {
		return "", errors.New("mobile: adapt panelId is required")
	}
	gen, err := c.newAdaptImageGenerator(req.Image)
	if err != nil {
		return "", err
	}
	opts, err := req.Options.toAdaptOptions()
	if err != nil {
		return "", err
	}

	ctx, done := c.ctx(callID)
	defer done()

	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, _ string, _ []byte) (string, error) {
		return fmt.Sprintf("%s/%s/%s", meta.ChapterID, meta.SceneID, meta.PanelID), nil
	})

	res, err := adapt.RenderPanel(ctx, req.Project, req.ChapterID, req.SceneID, req.PanelID, gen, sink, opts)
	if err != nil {
		return "", err
	}

	out := renderPanelResponseWire{
		Project: req.Project,
		SceneID: res.SceneID,
		PanelID: res.PanelID,
		RefID:   res.RefID,
		Skipped: res.Skipped,
		Usage:   res.Usage,
	}
	if res.Image != nil {
		out.MIMEType = res.Image.MIMEType
		if len(res.Image.Data) > 0 {
			out.DataBase64 = base64.StdEncoding.EncodeToString(res.Image.Data)
		}
		out.RevisedPrompt = res.Image.RevisedPrompt
	}
	return encode(out)
}

// RenderStoryboard renders every pending panel of chapterId in order over
// Core's host bridge (Cookies: false). Already-done panels are skipped unless
// options.forceRerender is true. When rendering stops partway after at least
// one panel was rendered or skipped (for example on budget exhaustion,
// cancellation, or a blocked panel), the JSON response carries completed=false,
// error/errorCode, the updated project, and all rendered panel images so the
// client can persist them and resume later.
func (c *Core) RenderStoryboard(callID int64, requestJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)

	var req renderStoryboardRequestWire
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("mobile: invalid render storyboard request JSON: %w", err)
	}
	if req.Project == nil {
		return "", errors.New("mobile: adapt project is required")
	}
	if strings.TrimSpace(req.ChapterID) == "" {
		return "", errors.New("mobile: adapt chapterId is required")
	}
	gen, err := c.newAdaptImageGenerator(req.Image)
	if err != nil {
		return "", err
	}
	opts, err := req.Options.toAdaptOptions()
	if err != nil {
		return "", err
	}

	ctx, done := c.ctx(callID)
	defer done()

	var images []renderedPanelImageWire
	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, mimeType string, data []byte) (string, error) {
		refID := fmt.Sprintf("%s/%s/%s", meta.ChapterID, meta.SceneID, meta.PanelID)
		images = append(images, renderedPanelImageWire{
			SceneID:    meta.SceneID,
			PanelID:    meta.PanelID,
			RefID:      refID,
			MIMEType:   mimeType,
			DataBase64: base64.StdEncoding.EncodeToString(data),
		})
		return refID, nil
	})

	res, renderErr := adapt.RenderStoryboard(ctx, req.Project, req.ChapterID, gen, sink, opts)
	if renderErr != nil {
		if res != nil && (res.Rendered > 0 || res.Skipped > 0) {
			return encode(renderStoryboardResponseWire{
				Completed: false,
				Project:   req.Project,
				Result:    toRenderSummaryWire(res),
				Images:    images,
				Error:     renderErr.Error(),
				ErrorCode: adaptErrorCode(renderErr),
			})
		}
		return "", renderErr
	}

	return encode(renderStoryboardResponseWire{
		Completed: res.Failed == 0,
		Project:   req.Project,
		Result:    toRenderSummaryWire(res),
		Images:    images,
	})
}

func (c *Core) newAdaptTextGenerator(cfg adaptTextConfigWire) (*adapt.AIAPIGenerator, error) {
	proto := aiapi.Protocol(strings.ToLower(strings.TrimSpace(string(cfg.Protocol))))
	switch proto {
	case aiapi.Chat, aiapi.Responses, aiapi.Messages:
	default:
		return nil, fmt.Errorf("mobile: unsupported adapt text protocol %q", cfg.Protocol)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("mobile: adapt text apiKey is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("mobile: adapt text model is required")
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
		Transport:       c.bridge,
	}, nil
}

func (c *Core) newAdaptImageGenerator(cfg adaptImageConfigWire) (adapt.ImageGenerator, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("mobile: adapt image apiKey is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("mobile: adapt image model is required")
	}
	switch provider {
	case "gemini":
		return &adapt.GeminiImageGenerator{
			BaseURL:            strings.TrimSpace(cfg.BaseURL),
			APIKey:             strings.TrimSpace(cfg.APIKey),
			Model:              strings.TrimSpace(cfg.Model),
			ResponseModalities: append([]string(nil), cfg.ResponseModalities...),
			Partition:          strings.TrimSpace(cfg.Partition),
			Transport:          c.bridge,
		}, nil
	case "openai":
		return &adapt.OpenAIImageGenerator{
			BaseURL:        strings.TrimSpace(cfg.BaseURL),
			APIKey:         strings.TrimSpace(cfg.APIKey),
			Model:          strings.TrimSpace(cfg.Model),
			Size:           strings.TrimSpace(cfg.Size),
			Quality:        strings.TrimSpace(cfg.Quality),
			OutputFormat:   strings.TrimSpace(cfg.OutputFormat),
			ResponseFormat: strings.TrimSpace(cfg.ResponseFormat),
			Partition:      strings.TrimSpace(cfg.Partition),
			Transport:      c.bridge,
		}, nil
	default:
		return nil, fmt.Errorf("mobile: unsupported adapt image provider %q", cfg.Provider)
	}
}

func (w adaptRenderOptionsWire) toAdaptOptions() (adapt.RenderOptions, error) {
	opts := adapt.RenderOptions{
		AspectRatio:           w.AspectRatio,
		Size:                  w.Size,
		MaxRetries:            w.MaxRetries,
		ForceRerender:         w.ForceRerender,
		ContinueOnError:       w.ContinueOnError,
		FeedbackCharacterRefs: w.FeedbackCharacterRefs,
	}
	if len(w.StyleReferences) > 0 {
		opts.StyleReferences = make([]adapt.ImageRef, 0, len(w.StyleReferences))
		for _, r := range w.StyleReferences {
			ref, err := r.toAdaptRef()
			if err != nil {
				return adapt.RenderOptions{}, err
			}
			opts.StyleReferences = append(opts.StyleReferences, ref)
		}
	}
	if len(w.CharacterReferences) > 0 {
		opts.CharacterReferences = make(map[string][]adapt.ImageRef, len(w.CharacterReferences))
		for key, list := range w.CharacterReferences {
			refs := make([]adapt.ImageRef, 0, len(list))
			for _, r := range list {
				ref, err := r.toAdaptRef()
				if err != nil {
					return adapt.RenderOptions{}, err
				}
				refs = append(refs, ref)
			}
			opts.CharacterReferences[key] = refs
		}
	}
	return opts, nil
}

func (r adaptImageRefWire) toAdaptRef() (adapt.ImageRef, error) {
	rawB64 := strings.TrimSpace(r.DataBase64)
	if rawB64 == "" {
		rawB64 = strings.TrimSpace(r.Data)
	}
	var decoded []byte
	if rawB64 != "" {
		var err error
		decoded, err = base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			return adapt.ImageRef{}, fmt.Errorf("mobile: invalid adapt reference image base64: %w", err)
		}
	}
	return adapt.ImageRef{
		ID:       r.ID,
		Kind:     r.Kind,
		Label:    r.Label,
		MIMEType: r.MIMEType,
		Data:     decoded,
	}, nil
}

func toRenderSummaryWire(res *adapt.RenderResult) renderSummaryWire {
	out := renderSummaryWire{
		ChapterID: res.ChapterID,
		Rendered:  res.Rendered,
		Skipped:   res.Skipped,
		Failed:    res.Failed,
		Blocked:   res.Blocked,
		Usage:     res.Usage,
	}
	if len(res.Failures) > 0 {
		out.Failures = make([]panelFailureWire, len(res.Failures))
		for i, f := range res.Failures {
			msg := ""
			if f.Err != nil {
				msg = f.Err.Error()
			}
			out.Failures[i] = panelFailureWire{
				SceneID:   f.SceneID,
				PanelID:   f.PanelID,
				Error:     msg,
				ErrorCode: adaptErrorCode(f.Err),
			}
		}
	}
	return out
}

func adaptErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, adapt.ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, adapt.ErrContentBlocked):
		return "content_blocked"
	case errors.Is(err, adapt.ErrInvalidStoryboard):
		return "invalid_storyboard"
	case errors.Is(err, adapt.ErrEmptyChapter):
		return "empty_chapter"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "error"
	}
}

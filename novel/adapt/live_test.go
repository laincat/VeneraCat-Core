//go:build live

package adapt_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

type liveHTTPTransport struct {
	client *http.Client
}

func (t *liveHTTPTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for k, vals := range req.Header {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &host.Response{
		Status:   resp.StatusCode,
		Header:   resp.Header.Clone(),
		Body:     body,
		FinalURL: resp.Request.URL.String(),
	}, nil
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func liveSampleChapter() (novel.Chapter, novel.ChapterContent) {
	return novel.Chapter{
		ID:    "live-ch1",
		Title: "Night Rain at the Doorway",
	}, novel.ChapterContent{
		HTML: `<p>Rain tapped steadily against the hallway window. Lin Qing paused outside apartment 402, her dark hair damp around her shoulders, fingers resting on a brass spare key.</p>
<p>"I am here," she called softly through the door. Warm amber light spilled onto the wooden floor as the latch clicked open.</p>`,
	}
}

func TestLiveDeepSeekAdaptChapter(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_DEEPSEEK_KEY"))
	if apiKey == "" {
		t.Skip("set VENERACAT_LIVE_DEEPSEEK_KEY to run live DeepSeek smoke test")
	}
	baseURL := envOrDefault("VENERACAT_LIVE_DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1")
	model := envOrDefault("VENERACAT_LIVE_DEEPSEEK_MODEL", "deepseek-chat")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	proj := &adapt.Project{
		ID:     "live-deepseek",
		Title:  "Night Rain Smoke",
		Budget: adapt.Budget{MaxCalls: 2, MaxTotalTokens: 16000},
	}
	gen := &adapt.AIAPIGenerator{
		Config: aiapi.Config{
			Protocol: aiapi.Chat,
			BaseURL:  baseURL,
			APIKey:   apiKey,
		},
		Model:     model,
		MaxTokens: 2048,
		Transport: &liveHTTPTransport{client: &http.Client{Timeout: 80 * time.Second}},
	}

	ch, content := liveSampleChapter()
	res, err := adapt.AdaptChapter(ctx, proj, ch, content, gen, adapt.AdaptOptions{MaxChunkRunes: 1200})
	if err != nil {
		t.Fatalf("DeepSeek AdaptChapter failed (model=%s): %v", model, err)
	}
	if len(res.Storyboard.Scenes) == 0 || len(res.Storyboard.Scenes[0].Panels) == 0 {
		t.Fatalf("DeepSeek returned empty storyboard")
	}
	t.Logf("DeepSeek OK: model=%s scenes=%d characters=%d usage=%+v", model, len(res.Storyboard.Scenes), len(proj.Characters.Characters), res.Usage)
}

func TestLiveCodexAdaptChapter(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_CODEX_KEY"))
	if apiKey == "" {
		t.Skip("set VENERACAT_LIVE_CODEX_KEY to run live Codex smoke test")
	}
	baseURL := envOrDefault("VENERACAT_LIVE_CODEX_BASE_URL", "https://api.openai.com/v1")
	model := envOrDefault("VENERACAT_LIVE_CODEX_MODEL", "gpt-5-codex")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	proj := &adapt.Project{
		ID:     "live-codex",
		Title:  "Night Rain Smoke",
		Budget: adapt.Budget{MaxCalls: 2, MaxTotalTokens: 16000},
	}
	gen := &adapt.AIAPIGenerator{
		Config: aiapi.Config{
			Protocol: aiapi.Responses,
			BaseURL:  baseURL,
			APIKey:   apiKey,
		},
		Model:     model,
		MaxTokens: 2048,
		Transport: &liveHTTPTransport{client: &http.Client{Timeout: 80 * time.Second}},
	}

	ch, content := liveSampleChapter()
	res, err := adapt.AdaptChapter(ctx, proj, ch, content, gen, adapt.AdaptOptions{MaxChunkRunes: 1200})
	if err != nil {
		t.Fatalf("Codex AdaptChapter failed (model=%s): %v", model, err)
	}
	if len(res.Storyboard.Scenes) == 0 || len(res.Storyboard.Scenes[0].Panels) == 0 {
		t.Fatalf("Codex returned empty storyboard")
	}
	t.Logf("Codex OK: model=%s scenes=%d characters=%d usage=%+v", model, len(res.Storyboard.Scenes), len(proj.Characters.Characters), res.Usage)
}

func TestLiveGeminiRenderPanel(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_GEMINI_KEY"))
	if apiKey == "" {
		t.Skip("set VENERACAT_LIVE_GEMINI_KEY to run live Gemini image smoke test")
	}
	baseURL := envOrDefault("VENERACAT_LIVE_GEMINI_BASE_URL", adapt.DefaultGeminiBaseURL)
	model := envOrDefault("VENERACAT_LIVE_GEMINI_IMAGE_MODEL", "gemini-2.5-flash-image")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	proj := &adapt.Project{
		ID:     "live-gemini-image",
		Title:  "Night Rain Smoke",
		Budget: adapt.Budget{MaxImageCalls: 1},
		StyleRef: &adapt.StyleRef{
			StyleNotes: "clean anime ink lines, warm amber doorway lighting, rainy night atmosphere",
		},
		Characters: adapt.CharacterSheet{
			Characters: []adapt.Character{
				{
					ID:         "c1",
					Name:       "Lin Qing",
					Appearance: "long dark hair, calm dark eyes",
					Outfit:     "light beige trench coat",
				},
			},
		},
		Storyboards: map[string]*adapt.Storyboard{
			"live-ch1": {
				ChapterID: "live-ch1",
				Scenes: []adapt.Scene{
					{
						ID:      "s1",
						Setting: "Rainy apartment hallway",
						Mood:    "quiet suspense",
						Panels: []adapt.Panel{
							{
								ID:          "p1",
								Shot:        "medium shot",
								Action:      "Lin Qing holds a brass key outside a slightly open wooden door with warm interior light",
								Characters:  []string{"Lin Qing"},
								Prompt:      "static comic panel illustration of a young woman outside an apartment doorway at night",
								ImageStatus: adapt.ImagePending,
							},
						},
					},
				},
			},
		},
	}

	gen := &adapt.GeminiImageGenerator{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Model:     model,
		Transport: &liveHTTPTransport{client: &http.Client{Timeout: 110 * time.Second}},
	}

	var savedBytes int
	var savedMIME string
	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, mimeType string, data []byte) (string, error) {
		savedBytes = len(data)
		savedMIME = mimeType
		return "live://" + meta.ChapterID + "/" + meta.PanelID, nil
	})

	res, err := adapt.RenderPanel(ctx, proj, "live-ch1", "s1", "p1", gen, sink, adapt.RenderOptions{
		AspectRatio: "3:4",
	})
	if err != nil {
		t.Fatalf("Gemini RenderPanel failed (model=%s): %v", model, err)
	}
	if savedBytes == 0 || !strings.HasPrefix(savedMIME, "image/") {
		t.Fatalf("unexpected rendered image: mime=%q bytes=%d", savedMIME, savedBytes)
	}
	t.Logf("Gemini image OK: model=%s mime=%s bytes=%d usage=%+v", model, savedMIME, savedBytes, res.Usage)
}

func TestLiveOpenAIImageRenderPanel(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_OPENAI_IMAGE_KEY"))
	if apiKey == "" {
		t.Skip("set VENERACAT_LIVE_OPENAI_IMAGE_KEY to run live OpenAI Images smoke test")
	}
	baseURL := envOrDefault("VENERACAT_LIVE_OPENAI_BASE_URL", adapt.DefaultOpenAIImageBaseURL)
	model := envOrDefault("VENERACAT_LIVE_OPENAI_IMAGE_MODEL", "gpt-image-1")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	proj := &adapt.Project{
		ID:     "live-openai-image",
		Title:  "Night Rain Smoke",
		Budget: adapt.Budget{MaxImageCalls: 1},
		Storyboards: map[string]*adapt.Storyboard{
			"live-ch1": {
				ChapterID: "live-ch1",
				Scenes: []adapt.Scene{
					{
						ID:      "s1",
						Setting: "Rainy apartment hallway",
						Panels: []adapt.Panel{
							{
								ID:          "p1",
								Shot:        "medium shot",
								Action:      "Woman pauses outside a warm lit wooden doorway",
								Prompt:      "clean anime comic panel of a rainy hallway doorway with warm amber light",
								ImageStatus: adapt.ImagePending,
							},
						},
					},
				},
			},
		},
	}

	gen := &adapt.OpenAIImageGenerator{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Model:        model,
		Size:         "1024x1024",
		OutputFormat: "png",
		Transport:    &liveHTTPTransport{client: &http.Client{Timeout: 110 * time.Second}},
	}

	res, err := adapt.RenderPanel(ctx, proj, "live-ch1", "s1", "p1", gen, nil, adapt.RenderOptions{})
	if err != nil {
		t.Fatalf("OpenAI RenderPanel failed (model=%s): %v", model, err)
	}
	if res.Image == nil || len(res.Image.Data) == 0 {
		t.Fatal("OpenAI returned empty image bytes")
	}
	t.Logf("OpenAI image OK: model=%s mime=%s bytes=%d usage=%+v", model, res.Image.MIMEType, len(res.Image.Data), res.Usage)
}

func TestLiveAdaptAndRenderSmoke(t *testing.T) {
	geminiKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_GEMINI_KEY"))
	deepseekKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_DEEPSEEK_KEY"))
	codexKey := strings.TrimSpace(os.Getenv("VENERACAT_LIVE_CODEX_KEY"))

	if geminiKey == "" || (deepseekKey == "" && codexKey == "") {
		t.Skip("set VENERACAT_LIVE_GEMINI_KEY and either VENERACAT_LIVE_DEEPSEEK_KEY or VENERACAT_LIVE_CODEX_KEY to run combined live smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	tr := &liveHTTPTransport{client: &http.Client{Timeout: 110 * time.Second}}
	var textGen adapt.TextGenerator
	var textProvider string
	if deepseekKey != "" {
		textProvider = "deepseek"
		textGen = &adapt.AIAPIGenerator{
			Config: aiapi.Config{
				Protocol: aiapi.Chat,
				BaseURL:  envOrDefault("VENERACAT_LIVE_DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
				APIKey:   deepseekKey,
			},
			Model:     envOrDefault("VENERACAT_LIVE_DEEPSEEK_MODEL", "deepseek-chat"),
			MaxTokens: 2048,
			Transport: tr,
		}
	} else {
		textProvider = "codex"
		textGen = &adapt.AIAPIGenerator{
			Config: aiapi.Config{
				Protocol: aiapi.Responses,
				BaseURL:  envOrDefault("VENERACAT_LIVE_CODEX_BASE_URL", "https://api.openai.com/v1"),
				APIKey:   codexKey,
			},
			Model:     envOrDefault("VENERACAT_LIVE_CODEX_MODEL", "gpt-5-codex"),
			MaxTokens: 2048,
			Transport: tr,
		}
	}

	proj := &adapt.Project{
		ID:     "live-e2e-smoke",
		Title:  "Night Rain Smoke",
		Budget: adapt.Budget{MaxCalls: 2, MaxImageCalls: 1, MaxTotalTokens: 20000},
	}

	ch, content := liveSampleChapter()
	adaptRes, err := adapt.AdaptChapter(ctx, proj, ch, content, textGen, adapt.AdaptOptions{MaxChunkRunes: 1200})
	if err != nil {
		t.Fatalf("combined AdaptChapter (%s) failed: %v", textProvider, err)
	}
	firstScene := adaptRes.Storyboard.Scenes[0]
	firstPanel := firstScene.Panels[0]

	imgGen := &adapt.GeminiImageGenerator{
		BaseURL:   envOrDefault("VENERACAT_LIVE_GEMINI_BASE_URL", adapt.DefaultGeminiBaseURL),
		APIKey:    geminiKey,
		Model:     envOrDefault("VENERACAT_LIVE_GEMINI_IMAGE_MODEL", "gemini-2.5-flash-image"),
		Transport: tr,
	}
	renderRes, err := adapt.RenderPanel(ctx, proj, ch.ID, firstScene.ID, firstPanel.ID, imgGen, nil, adapt.RenderOptions{
		AspectRatio: "3:4",
	})
	if err != nil {
		t.Fatalf("combined RenderPanel (gemini) failed: %v", err)
	}
	t.Logf("Combined live smoke OK: textProvider=%s panel=%s/%s mime=%s bytes=%d totalUsage=%+v",
		textProvider, firstScene.ID, firstPanel.ID, renderRes.Image.MIMEType, len(renderRes.Image.Data), proj.Usage)
}

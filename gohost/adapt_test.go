package gohost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

var adaptTestPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d,
	0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

func openAdaptTestCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestAdaptChapterGenerateAndCheckpointResumeViaTransport(t *testing.T) {
	c := openAdaptTestCore(t)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("AI adapt request must not send cookies, got %q", r.Header.Get("Cookie"))
		}
		if r.Header.Get("Authorization") != "Bearer test-adapt-key" {
			t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
		}
		n := calls.Add(1)
		var outJSON string
		if n == 1 {
			outJSON = `{"scenes":[{"setting":"Rainy porch","panels":[{"shot":"wide","action":"Boy opens door","prompt":"rainy porch doorway"}]}],"newCharacters":[{"id":"c_lin","name":"Lin","appearance":"black hair","outfit":"dark jacket"}]}`
		} else {
			outJSON = `{"scenes":[{"setting":"Dim hallway","panels":[{"shot":"close-up","action":"Key gleams","prompt":"brass key hallway"}]}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": outJSON}},
			},
			"usage": map[string]any{
				"prompt_tokens":     60,
				"completion_tokens": 40,
				"total_tokens":      100,
			},
		})
	}))
	defer srv.Close()

	// Seed a cookie in the same partition to verify Cookies: false never leaks it.
	c.Jar().Set("adapt-part", srv.URL+"/", []*http.Cookie{{Name: "sid", Value: "secret-cookie", Path: "/"}})

	proj := &adapt.Project{
		ID:     "proj-gohost-1",
		Title:  "Rainy Night",
		Budget: adapt.Budget{MaxCalls: 1},
	}
	chapter := novel.Chapter{ID: "ch1", Title: "Chapter 1"}
	content := novel.ChapterContent{HTML: "<p>第一段：夜雨敲窗，少年推门而出。</p><p>第二段：走廊尽头传来轻微的脚步声。</p>"}
	textCfg := AdaptTextConfig{
		Protocol:  aiapi.Chat,
		BaseURL:   srv.URL,
		APIKey:    "test-adapt-key",
		Partition: "adapt-part",
		Model:     "gpt-test",
		JSONMode:  true,
	}

	// Pass 1: MaxCalls=1 halts before chunk 2 with ErrBudgetExceeded + Checkpoint.
	res1, err := c.AdaptChapter(context.Background(), AdaptChapterRequest{
		Project: proj,
		Chapter: chapter,
		Content: content,
		Text:    textCfg,
		Options: adapt.AdaptOptions{MaxChunkRunes: 18},
	})
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("pass 1 error = %v, want ErrBudgetExceeded", err)
	}
	if res1 == nil || res1.Checkpoint == nil || res1.CompletedChunks != 1 || res1.Chunks != 2 {
		t.Fatalf("pass 1 partial result mismatch: %+v", res1)
	}
	if calls.Load() != 1 {
		t.Fatalf("pass 1 calls = %d, want 1", calls.Load())
	}

	// Pass 2: Raise budget and resume from Checkpoint; chunk 1 is skipped.
	proj.Budget.MaxCalls = 4
	res2, err := c.AdaptChapter(context.Background(), AdaptChapterRequest{
		Project: proj,
		Chapter: chapter,
		Content: content,
		Text:    textCfg,
		Options: adapt.AdaptOptions{Checkpoint: res1.Checkpoint},
	})
	if err != nil {
		t.Fatalf("pass 2 failed: %v", err)
	}
	if res2.Checkpoint != nil || res2.CompletedChunks != 2 || len(res2.Storyboard.Scenes) != 2 {
		t.Fatalf("pass 2 result mismatch: %+v", res2)
	}
	if calls.Load() != 2 {
		t.Fatalf("total calls across resume = %d, want 2 (no duplicate chunk 1)", calls.Load())
	}
	if len(proj.Characters.Characters) != 1 || proj.Storyboards["ch1"] == nil {
		t.Fatalf("project state incomplete: chars=%+v sb=%+v", proj.Characters.Characters, proj.Storyboards["ch1"])
	}
}

func TestRenderPanelAndStoryboardResumeForceRerenderAndReferences(t *testing.T) {
	c := openAdaptTestCore(t)

	var geminiCalls atomic.Int32
	var sawStyleInline, sawCharInline atomic.Bool
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Errorf("gemini image request must not send cookies")
		}
		if r.Header.Get("x-goog-api-key") != "gemini-test-key" {
			t.Errorf("unexpected gemini key header: %q", r.Header.Get("x-goog-api-key"))
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("Personal local style reference")) {
			sawStyleInline.Store(true)
		}
		if bytes.Contains(body, []byte("Character visual reference")) {
			sawCharInline.Store(true)
		}
		geminiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"finishReason": "STOP",
					"content": map[string]any{
						"parts": []map[string]any{
							{
								"inlineData": map[string]any{
									"mimeType": "image/png",
									"data":     base64.StdEncoding.EncodeToString(adaptTestPNG),
								},
							},
						},
					},
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     20,
				"candidatesTokenCount": 10,
				"totalTokenCount":      30,
			},
		})
	}))
	defer geminiSrv.Close()

	c.Jar().Set("img-part", geminiSrv.URL+"/", []*http.Cookie{{Name: "sid", Value: "no-send", Path: "/"}})

	proj := &adapt.Project{
		ID:    "proj-render",
		Title: "Ink Tale",
		Characters: adapt.CharacterSheet{
			Characters: []adapt.Character{
				{ID: "c1", Name: "Wen", Appearance: "long hair", Outfit: "trench coat"},
			},
		},
		Storyboards: map[string]*adapt.Storyboard{
			"ch1": {
				ChapterID: "ch1",
				Scenes: []adapt.Scene{
					{
						ID:      "s1",
						Setting: "Rainy hallway",
						Panels: []adapt.Panel{
							{ID: "p1", Shot: "wide", Action: "Enters", Characters: []string{"Wen"}, Prompt: "doorway", ImageStatus: adapt.ImagePending},
							{ID: "p2", Shot: "close", Action: "Holds key", Characters: []string{"Wen"}, Prompt: "brass key", ImageStatus: adapt.ImagePending},
						},
					},
				},
			},
		},
		Budget: adapt.Budget{MaxImageCalls: 1},
	}

	var savedMu sync.Mutex
	saved := map[string][]byte{}
	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, _ string, data []byte) (string, error) {
		savedMu.Lock()
		defer savedMu.Unlock()
		id := fmt.Sprintf("local:%s/%s/%s", meta.ChapterID, meta.SceneID, meta.PanelID)
		saved[id] = bytes.Clone(data)
		return id, nil
	})

	imgCfg := AdaptImageConfig{
		Provider:  AdaptImageGemini,
		BaseURL:   geminiSrv.URL,
		APIKey:    "gemini-test-key",
		Model:     "gemini-2.5-flash-image",
		Partition: "img-part",
	}
	renderOpts := adapt.RenderOptions{
		AspectRatio: "3:4",
		StyleReferences: []adapt.ImageRef{
			{ID: "style1", Kind: adapt.ImageRefStyle, Label: "watercolor", MIMEType: "image/png", Data: adaptTestPNG},
		},
		CharacterReferences: map[string][]adapt.ImageRef{
			"Wen": {{ID: "char1", Kind: adapt.ImageRefCharacter, Label: "Wen", MIMEType: "image/png", Data: adaptTestPNG}},
		},
	}

	// Pass 1: RenderStoryboard hits MaxImageCalls=1 after p1.
	rr1, err := c.RenderStoryboard(context.Background(), RenderStoryboardRequest{
		Project:   proj,
		ChapterID: "ch1",
		Image:     imgCfg,
		Options:   renderOpts,
		Sink:      sink,
	})
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("pass 1 error = %v, want ErrBudgetExceeded", err)
	}
	if rr1 == nil || rr1.Rendered != 1 || proj.Storyboards["ch1"].Scenes[0].Panels[0].ImageStatus != adapt.ImageDone {
		t.Fatalf("pass 1 unexpected render state: rr=%+v p1=%+v", rr1, proj.Storyboards["ch1"].Scenes[0].Panels[0])
	}
	if !sawStyleInline.Load() || !sawCharInline.Load() {
		t.Fatalf("expected style and character inline references to be sent (style=%v char=%v)", sawStyleInline.Load(), sawCharInline.Load())
	}

	// Pass 2: Raise budget and resume with ForceRerender=false; p1 skipped, p2 rendered.
	proj.Budget.MaxImageCalls = 5
	rr2, err := c.RenderStoryboard(context.Background(), RenderStoryboardRequest{
		Project:   proj,
		ChapterID: "ch1",
		Image:     imgCfg,
		Options:   renderOpts,
		Sink:      sink,
	})
	if err != nil {
		t.Fatalf("pass 2 failed: %v", err)
	}
	if rr2.Skipped != 1 || rr2.Rendered != 1 || geminiCalls.Load() != 2 {
		t.Fatalf("pass 2 resume mismatch: rr=%+v calls=%d", rr2, geminiCalls.Load())
	}

	// Single panel render with ForceRerender=false skips p1; ForceRerender=true re-renders p1.
	prSkip, err := c.RenderPanel(context.Background(), RenderPanelRequest{
		Project:   proj,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image:     imgCfg,
		Options:   renderOpts,
		Sink:      sink,
	})
	if err != nil || !prSkip.Skipped || geminiCalls.Load() != 2 {
		t.Fatalf("expected p1 skip, got res=%+v err=%v calls=%d", prSkip, err, geminiCalls.Load())
	}

	forceOpts := renderOpts
	forceOpts.ForceRerender = true
	prForce, err := c.RenderPanel(context.Background(), RenderPanelRequest{
		Project:   proj,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image:     imgCfg,
		Options:   forceOpts,
		Sink:      sink,
	})
	if err != nil || prForce.Skipped || geminiCalls.Load() != 3 {
		t.Fatalf("expected p1 force rerender, got res=%+v err=%v calls=%d", prForce, err, geminiCalls.Load())
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved panel refs, got %d", len(saved))
	}
}

func TestRenderPanelContentBlockedDoesNotRetry(t *testing.T) {
	c := openAdaptTestCore(t)

	var openaiCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"content_policy_violation","type":"image_generation_user_error","message":"blocked by safety system"}}`))
	}))
	defer srv.Close()

	proj := &adapt.Project{
		ID: "proj-blocked",
		Storyboards: map[string]*adapt.Storyboard{
			"ch1": {
				ChapterID: "ch1",
				Scenes: []adapt.Scene{{
					ID:      "s1",
					Setting: "Street",
					Panels:  []adapt.Panel{{ID: "p1", Shot: "wide", Action: "Run", Prompt: "street", ImageStatus: adapt.ImagePending}},
				}},
			},
		},
	}

	_, err := c.RenderPanel(context.Background(), RenderPanelRequest{
		Project:   proj,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image: AdaptImageConfig{
			Provider: AdaptImageOpenAI,
			BaseURL:  srv.URL,
			APIKey:   "openai-test-key",
			Model:    "gpt-image-1",
		},
		Options: adapt.RenderOptions{MaxRetries: 3},
	})
	if !errors.Is(err, adapt.ErrContentBlocked) {
		t.Fatalf("error = %v, want ErrContentBlocked", err)
	}
	if openaiCalls.Load() != 1 {
		t.Fatalf("ErrContentBlocked must never retry, got %d calls", openaiCalls.Load())
	}
	if proj.Usage.ImageCalls != 0 {
		t.Fatalf("ErrContentBlocked must not charge budget, got usage=%+v", proj.Usage)
	}
}

func TestAdaptCancellationAndValidation(t *testing.T) {
	c := openAdaptTestCore(t)

	// Slow upstream server blocks until request context is canceled or test releases it.
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		slowSrv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered
		cancel()
	}()

	start := time.Now()
	_, err := c.AdaptChapter(ctx, AdaptChapterRequest{
		Project: &adapt.Project{ID: "p-cancel", Title: "T"},
		Chapter: novel.Chapter{ID: "ch1", Title: "Ch1"},
		Content: novel.ChapterContent{HTML: "<p>Hello world.</p>"},
		Text: AdaptTextConfig{
			Protocol: aiapi.Chat,
			BaseURL:  slowSrv.URL,
			APIKey:   "secret-cancel-key",
			Model:    "gpt-test",
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation took too long: %v", time.Since(start))
	}
	if strings.Contains(err.Error(), "secret-cancel-key") {
		t.Fatalf("error leaked API key: %v", err)
	}

	// Validation errors.
	if _, err := c.AdaptChapter(context.Background(), AdaptChapterRequest{}); err == nil {
		t.Fatal("nil project should fail")
	}
	if _, err := c.AdaptChapter(context.Background(), AdaptChapterRequest{
		Project: &adapt.Project{ID: "p"},
		Text:    AdaptTextConfig{Protocol: "unknown", APIKey: "k", Model: "m"},
	}); err == nil {
		t.Fatal("unsupported protocol should fail")
	}
	if _, err := c.AdaptChapter(context.Background(), AdaptChapterRequest{
		Project: &adapt.Project{ID: "p"},
		Text:    AdaptTextConfig{Protocol: aiapi.Chat, Model: "m"},
	}); err == nil {
		t.Fatal("missing text apiKey should fail")
	}
	if _, err := c.RenderPanel(context.Background(), RenderPanelRequest{
		Project: &adapt.Project{ID: "p"},
	}); err == nil {
		t.Fatal("missing chapterId/panelId should fail")
	}
	if _, err := c.RenderStoryboard(context.Background(), RenderStoryboardRequest{
		Project:   &adapt.Project{ID: "p"},
		ChapterID: "ch1",
		Image:     AdaptImageConfig{Provider: "unknown", APIKey: "k", Model: "m"},
	}); err == nil {
		t.Fatal("unsupported image provider should fail")
	}
}

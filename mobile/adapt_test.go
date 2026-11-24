package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

var mobileTestPNG = []byte{
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

type adaptFakeHost struct {
	*fakeHost
	httpHandler func(r wireRequest, body []byte) (wireResponse, error)

	cancelCh chan int64
}

func newAdaptFakeHost(fn func(r wireRequest, body []byte) (wireResponse, error)) *adaptFakeHost {
	return &adaptFakeHost{
		fakeHost:    &fakeHost{data: map[string]string{}},
		httpHandler: fn,
		cancelCh:    make(chan int64, 8),
	}
}

func (h *adaptFakeHost) HTTP(reqJSON string) (string, error) {
	var r wireRequest
	if err := json.Unmarshal([]byte(reqJSON), &r); err != nil {
		return "", err
	}
	var body []byte
	if r.BodyBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(r.BodyBase64)
		if err != nil {
			return "", err
		}
		body = decoded
	}
	resp, err := h.httpHandler(r, body)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (h *adaptFakeHost) CancelHTTP(id int64) {
	select {
	case h.cancelCh <- id:
	default:
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

func TestMobileAdaptChapterAndCheckpointResume(t *testing.T) {
	var calls atomic.Int32
	h := newAdaptFakeHost(func(r wireRequest, body []byte) (wireResponse, error) {
		if r.Cookies {
			t.Errorf("mobile adapt request must set Cookies=false, got true for %s", r.URL)
		}
		n := calls.Add(1)
		var content string
		if n == 1 {
			content = `{"scenes":[{"setting":"Rainy street","panels":[{"shot":"wide","action":"Steps out","prompt":"rainy street"}]}],"newCharacters":[{"id":"c1","name":"Wen","appearance":"dark hair","outfit":"coat"}]}`
		} else {
			content = `{"scenes":[{"setting":"Hallway","panels":[{"shot":"close","action":"Holds key","prompt":"brass key"}]}]}`
		}
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}},
			},
			"usage": map[string]any{
				"prompt_tokens":     50,
				"completion_tokens": 30,
				"total_tokens":      80,
			},
		})
		return wireResponse{
			Status:     http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			BodyBase64: base64.StdEncoding.EncodeToString(payload),
			FinalURL:   r.URL,
		}, nil
	})

	c, err := New(`{"locale":"zh_CN"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	chapter := novel.Chapter{ID: "ch1", Title: "Chapter 1"}
	content := novel.ChapterContent{HTML: "<p>第一段：夜雨敲窗，少年推门而出。</p><p>第二段：走廊尽头微弱的灯光摇曳。</p>"}
	textCfg := adaptTextConfigWire{
		Protocol: aiapi.Chat,
		BaseURL:  "https://ai.example.test/v1",
		APIKey:   "mobile-adapt-key",
		Model:    "gpt-test",
		JSONMode: true,
	}

	// Pass 1: MaxCalls=1 stops after chunk 1 and returns completed=false + checkpoint.
	req1 := adaptChapterRequestWire{
		Project: &adapt.Project{
			ID:     "proj-mobile-1",
			Title:  "Rainy Night",
			Budget: adapt.Budget{MaxCalls: 1},
		},
		Chapter: chapter,
		Content: content,
		Text:    textCfg,
		Options: adapt.AdaptOptions{MaxChunkRunes: 18},
	}
	out1, err := c.AdaptChapter(c.NewCall(), mustMarshal(t, req1))
	if err != nil {
		t.Fatalf("pass 1 returned hard error instead of partial checkpoint JSON: %v", err)
	}
	var resp1 adaptChapterResponseWire
	if err := json.Unmarshal([]byte(out1), &resp1); err != nil {
		t.Fatalf("unmarshal pass 1: %v", err)
	}
	if resp1.Completed || resp1.ErrorCode != "budget_exceeded" {
		t.Fatalf("pass 1 expected budget_exceeded partial response, got %+v", resp1)
	}
	if resp1.Result == nil || resp1.Result.Checkpoint == nil || resp1.Result.CompletedChunks != 1 {
		t.Fatalf("pass 1 missing checkpoint: %+v", resp1.Result)
	}
	if calls.Load() != 1 {
		t.Fatalf("pass 1 calls = %d, want 1", calls.Load())
	}

	// Pass 2: Raise budget and pass checkpoint back -> skips chunk 1 and completes.
	resp1.Project.Budget.MaxCalls = 4
	req2 := adaptChapterRequestWire{
		Project: resp1.Project,
		Chapter: chapter,
		Content: content,
		Text:    textCfg,
		Options: adapt.AdaptOptions{Checkpoint: resp1.Result.Checkpoint},
	}
	out2, err := c.AdaptChapter(c.NewCall(), mustMarshal(t, req2))
	if err != nil {
		t.Fatalf("pass 2 failed: %v", err)
	}
	var resp2 adaptChapterResponseWire
	if err := json.Unmarshal([]byte(out2), &resp2); err != nil {
		t.Fatalf("unmarshal pass 2: %v", err)
	}
	if !resp2.Completed || resp2.ErrorCode != "" || resp2.Result.Checkpoint != nil {
		t.Fatalf("pass 2 expected completed response, got %+v", resp2)
	}
	if calls.Load() != 2 || len(resp2.Project.Storyboards["ch1"].Scenes) != 2 || len(resp2.Project.Characters.Characters) != 1 {
		t.Fatalf("pass 2 state mismatch: calls=%d sb=%+v chars=%+v", calls.Load(), resp2.Project.Storyboards["ch1"], resp2.Project.Characters.Characters)
	}

	// Zero-chunk budget exceeded returns direct error wrapping ErrBudgetExceeded.
	reqZero := adaptChapterRequestWire{
		Project: &adapt.Project{
			ID:     "proj-zero",
			Title:  "Zero",
			Budget: adapt.Budget{MaxCalls: 1},
			Usage:  adapt.Usage{Calls: 1},
		},
		Chapter: chapter,
		Content: content,
		Text:    textCfg,
	}
	if _, err := c.AdaptChapter(c.NewCall(), mustMarshal(t, reqZero)); !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("pre-exhausted budget expected ErrBudgetExceeded, got %v", err)
	}
}

func TestMobileRenderPanelAndStoryboardResumeForceRerenderAndRefs(t *testing.T) {
	var calls atomic.Int32
	var sawStyleRef, sawCharRef atomic.Bool
	h := newAdaptFakeHost(func(r wireRequest, body []byte) (wireResponse, error) {
		if r.Cookies {
			t.Errorf("mobile image request must set Cookies=false")
		}
		if bytes.Contains(body, []byte("Personal local style reference")) {
			sawStyleRef.Store(true)
		}
		if bytes.Contains(body, []byte("Character visual reference")) {
			sawCharRef.Store(true)
		}
		calls.Add(1)
		payload, _ := json.Marshal(map[string]any{
			"candidates": []map[string]any{
				{
					"finishReason": "STOP",
					"content": map[string]any{
						"parts": []map[string]any{
							{
								"inlineData": map[string]any{
									"mimeType": "image/png",
									"data":     base64.StdEncoding.EncodeToString(mobileTestPNG),
								},
							},
						},
					},
				},
			},
		})
		return wireResponse{
			Status:     http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			BodyBase64: base64.StdEncoding.EncodeToString(payload),
			FinalURL:   r.URL,
		}, nil
	})

	c, err := New(`{"locale":"zh_CN"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	proj := &adapt.Project{
		ID:    "proj-m-render",
		Title: "Ink",
		Characters: adapt.CharacterSheet{
			Characters: []adapt.Character{{ID: "c1", Name: "Wen", Appearance: "dark hair"}},
		},
		Storyboards: map[string]*adapt.Storyboard{
			"ch1": {
				ChapterID: "ch1",
				Scenes: []adapt.Scene{{
					ID:      "s1",
					Setting: "Hallway",
					Panels: []adapt.Panel{
						{ID: "p1", Shot: "wide", Action: "Walks", Characters: []string{"Wen"}, Prompt: "hallway", ImageStatus: adapt.ImagePending},
						{ID: "p2", Shot: "close", Action: "Looks", Characters: []string{"Wen"}, Prompt: "portrait", ImageStatus: adapt.ImagePending},
					},
				}},
			},
		},
		Budget: adapt.Budget{MaxImageCalls: 1},
	}

	pngB64 := base64.StdEncoding.EncodeToString(mobileTestPNG)
	imgCfg := adaptImageConfigWire{
		Provider: "gemini",
		BaseURL:  "https://gemini.example.test/v1beta",
		APIKey:   "gemini-mobile-key",
		Model:    "gemini-2.5-flash-image",
	}
	opts := adaptRenderOptionsWire{
		AspectRatio: "3:4",
		StyleReferences: []adaptImageRefWire{
			{ID: "st1", Kind: adapt.ImageRefStyle, Label: "ink", MIMEType: "image/png", DataBase64: pngB64},
		},
		CharacterReferences: map[string][]adaptImageRefWire{
			"Wen": {{ID: "ch1", Kind: adapt.ImageRefCharacter, Label: "Wen", MIMEType: "image/png", DataBase64: pngB64}},
		},
	}

	// Pass 1: MaxImageCalls=1 stops after p1 and returns partial JSON with p1 bytes.
	out1, err := c.RenderStoryboard(c.NewCall(), mustMarshal(t, renderStoryboardRequestWire{
		Project:   proj,
		ChapterID: "ch1",
		Image:     imgCfg,
		Options:   opts,
	}))
	if err != nil {
		t.Fatalf("pass 1 RenderStoryboard error: %v", err)
	}
	var sb1 renderStoryboardResponseWire
	if err := json.Unmarshal([]byte(out1), &sb1); err != nil {
		t.Fatalf("unmarshal pass 1: %v", err)
	}
	if sb1.Completed || sb1.ErrorCode != "budget_exceeded" || sb1.Result.Rendered != 1 || len(sb1.Images) != 1 {
		t.Fatalf("pass 1 unexpected result: %+v", sb1)
	}
	if sb1.Images[0].DataBase64 != pngB64 {
		t.Fatalf("p1 image bytes mismatch")
	}
	if !sawStyleRef.Load() || !sawCharRef.Load() {
		t.Fatalf("expected style and character refs on wire (style=%v char=%v)", sawStyleRef.Load(), sawCharRef.Load())
	}

	// Pass 2: Raise budget and resume with ForceRerender=false -> skips p1, renders p2.
	sb1.Project.Budget.MaxImageCalls = 5
	out2, err := c.RenderStoryboard(c.NewCall(), mustMarshal(t, renderStoryboardRequestWire{
		Project:   sb1.Project,
		ChapterID: "ch1",
		Image:     imgCfg,
		Options:   opts,
	}))
	if err != nil {
		t.Fatalf("pass 2 RenderStoryboard error: %v", err)
	}
	var sb2 renderStoryboardResponseWire
	if err := json.Unmarshal([]byte(out2), &sb2); err != nil {
		t.Fatalf("unmarshal pass 2: %v", err)
	}
	if !sb2.Completed || sb2.Result.Skipped != 1 || sb2.Result.Rendered != 1 || len(sb2.Images) != 1 || calls.Load() != 2 {
		t.Fatalf("pass 2 unexpected result: %+v calls=%d", sb2, calls.Load())
	}

	// RenderPanel skips p1 when ForceRerender=false.
	pSkipRaw, err := c.RenderPanel(c.NewCall(), mustMarshal(t, renderPanelRequestWire{
		Project:   sb2.Project,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image:     imgCfg,
		Options:   opts,
	}))
	if err != nil {
		t.Fatalf("RenderPanel skip error: %v", err)
	}
	var pSkip renderPanelResponseWire
	if err := json.Unmarshal([]byte(pSkipRaw), &pSkip); err != nil {
		t.Fatal(err)
	}
	if !pSkip.Skipped || calls.Load() != 2 {
		t.Fatalf("expected p1 skipped, got %+v calls=%d", pSkip, calls.Load())
	}

	// RenderPanel with ForceRerender=true re-renders p1.
	forceOpts := opts
	forceOpts.ForceRerender = true
	pForceRaw, err := c.RenderPanel(c.NewCall(), mustMarshal(t, renderPanelRequestWire{
		Project:   sb2.Project,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image:     imgCfg,
		Options:   forceOpts,
	}))
	if err != nil {
		t.Fatalf("RenderPanel force error: %v", err)
	}
	var pForce renderPanelResponseWire
	if err := json.Unmarshal([]byte(pForceRaw), &pForce); err != nil {
		t.Fatal(err)
	}
	if pForce.Skipped || pForce.DataBase64 != pngB64 || calls.Load() != 3 {
		t.Fatalf("expected p1 re-rendered, got skipped=%v calls=%d", pForce.Skipped, calls.Load())
	}
}

func TestMobileRenderContentBlockedDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	h := newAdaptFakeHost(func(r wireRequest, _ []byte) (wireResponse, error) {
		n := calls.Add(1)
		if strings.Contains(r.URL, "openai.example.test") {
			return wireResponse{
				Status:     http.StatusBadRequest,
				Headers:    http.Header{"Content-Type": {"application/json"}},
				BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"error":{"code":"content_policy_violation","type":"image_generation_user_error","message":"blocked by safety system"}}`)),
				FinalURL:   r.URL,
			}, nil
		}
		// Gemini storyboard test: panel 1 succeeds, panel 2 blocked by IMAGE_SAFETY.
		if n == 2 {
			okBody, _ := json.Marshal(map[string]any{
				"candidates": []map[string]any{{
					"finishReason": "STOP",
					"content": map[string]any{"parts": []map[string]any{{
						"inlineData": map[string]any{
							"mimeType": "image/png",
							"data":     base64.StdEncoding.EncodeToString(mobileTestPNG),
						},
					}}},
				}},
			})
			return wireResponse{Status: http.StatusOK, BodyBase64: base64.StdEncoding.EncodeToString(okBody), FinalURL: r.URL}, nil
		}
		blockedBody, _ := json.Marshal(map[string]any{
			"candidates": []map[string]any{{"finishReason": "IMAGE_SAFETY"}},
		})
		return wireResponse{Status: http.StatusOK, BodyBase64: base64.StdEncoding.EncodeToString(blockedBody), FinalURL: r.URL}, nil
	})

	c, err := New(`{"locale":"zh_CN"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	proj := &adapt.Project{
		ID: "p-block",
		Storyboards: map[string]*adapt.Storyboard{
			"ch1": {
				ChapterID: "ch1",
				Scenes: []adapt.Scene{{
					ID:      "s1",
					Setting: "Street",
					Panels: []adapt.Panel{
						{ID: "p1", Shot: "wide", Action: "Walk", Prompt: "street", ImageStatus: adapt.ImagePending},
						{ID: "p2", Shot: "close", Action: "Fight", Prompt: "duel", ImageStatus: adapt.ImagePending},
					},
				}},
			},
		},
	}

	// Single panel blocked on OpenAI must never retry even with MaxRetries=3.
	_, err = c.RenderPanel(c.NewCall(), mustMarshal(t, renderPanelRequestWire{
		Project:   proj,
		ChapterID: "ch1",
		SceneID:   "s1",
		PanelID:   "p1",
		Image: adaptImageConfigWire{
			Provider: "openai",
			BaseURL:  "https://openai.example.test/v1",
			APIKey:   "openai-secret-key",
			Model:    "gpt-image-1",
		},
		Options: adaptRenderOptionsWire{MaxRetries: 3},
	}))
	if !errors.Is(err, adapt.ErrContentBlocked) {
		t.Fatalf("RenderPanel error = %v, want ErrContentBlocked", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("ErrContentBlocked must not retry, got %d calls", calls.Load())
	}
	if strings.Contains(err.Error(), "openai-secret-key") {
		t.Fatalf("error leaked api key: %v", err)
	}

	// Reset p1 status and run RenderStoryboard with continueOnError=true on Gemini:
	// p1 renders, p2 blocked -> returns JSON preserving p1 image + blocked failure metadata.
	proj.Storyboards["ch1"].Scenes[0].Panels[0].ImageStatus = adapt.ImagePending
	out, err := c.RenderStoryboard(c.NewCall(), mustMarshal(t, renderStoryboardRequestWire{
		Project:   proj,
		ChapterID: "ch1",
		Image: adaptImageConfigWire{
			Provider: "gemini",
			BaseURL:  "https://gemini.example.test/v1beta",
			APIKey:   "gemini-secret-key",
			Model:    "gemini-2.5-flash-image",
		},
		Options: adaptRenderOptionsWire{ContinueOnError: true, MaxRetries: 2},
	}))
	if err != nil {
		t.Fatalf("RenderStoryboard continueOnError unexpected error: %v", err)
	}
	var sbResp renderStoryboardResponseWire
	if err := json.Unmarshal([]byte(out), &sbResp); err != nil {
		t.Fatal(err)
	}
	if sbResp.Completed || sbResp.Result.Rendered != 1 || sbResp.Result.Blocked != 1 || len(sbResp.Images) != 1 {
		t.Fatalf("unexpected storyboard response: %+v", sbResp)
	}
	if len(sbResp.Result.Failures) != 1 || sbResp.Result.Failures[0].ErrorCode != "content_blocked" {
		t.Fatalf("expected content_blocked failure metadata, got %+v", sbResp.Result.Failures)
	}
	// Total calls: 1 (openai blocked) + 1 (gemini p1) + 1 (gemini p2 blocked, no retries) = 3.
	if calls.Load() != 3 {
		t.Fatalf("expected 3 total HTTP calls (no retry on blocked), got %d", calls.Load())
	}
}

func TestMobileAdaptCancellationAndValidation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newAdaptFakeHost(func(r wireRequest, _ []byte) (wireResponse, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return wireResponse{}, errors.New("aborted")
	})

	c, err := New(`{"locale":"zh_CN"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(release)
		_ = c.Close()
	}()

	callID := c.NewCall()
	reqJSON := mustMarshal(t, adaptChapterRequestWire{
		Project: &adapt.Project{ID: "p-cancel", Title: "Cancel"},
		Chapter: novel.Chapter{ID: "ch1", Title: "Ch1"},
		Content: novel.ChapterContent{HTML: "<p>Hello world.</p>"},
		Text: adaptTextConfigWire{
			Protocol: aiapi.Chat,
			BaseURL:  "https://ai.example.test/v1",
			APIKey:   "cancel-key",
			Model:    "gpt-test",
		},
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := c.AdaptChapter(callID, reqJSON)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("AdaptChapter did not reach host HTTP")
	}

	c.Cancel(callID)
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled AdaptChapter error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AdaptChapter did not abort promptly after Cancel(callID)")
	}
	select {
	case <-h.cancelCh:
	case <-time.After(3 * time.Second):
		t.Fatal("CancelHTTP was not propagated to Host")
	}

	// Invalid arguments across all three methods must fail and release callIDs.
	badCalls := []struct {
		name string
		fn   func(id int64) error
	}{
		{"adapt-bad-json", func(id int64) error { _, err := c.AdaptChapter(id, "{bad"); return err }},
		{"adapt-nil-project", func(id int64) error { _, err := c.AdaptChapter(id, `{}`); return err }},
		{"adapt-bad-protocol", func(id int64) error {
			_, err := c.AdaptChapter(id, `{"project":{"id":"p"},"text":{"protocol":"bad","apiKey":"k","model":"m"}}`)
			return err
		}},
		{"panel-bad-json", func(id int64) error { _, err := c.RenderPanel(id, "{bad"); return err }},
		{"panel-missing-ids", func(id int64) error { _, err := c.RenderPanel(id, `{"project":{"id":"p"}}`); return err }},
		{"panel-bad-base64", func(id int64) error {
			_, err := c.RenderPanel(id, `{"project":{"id":"p"},"chapterId":"ch1","panelId":"p1","image":{"provider":"gemini","apiKey":"k","model":"m"},"options":{"styleReferences":[{"dataBase64":"!!!"}]}}`)
			return err
		}},
		{"storyboard-bad-json", func(id int64) error { _, err := c.RenderStoryboard(id, "{bad"); return err }},
		{"storyboard-bad-provider", func(id int64) error {
			_, err := c.RenderStoryboard(id, `{"project":{"id":"p"},"chapterId":"ch1","image":{"provider":"bad","apiKey":"k","model":"m"}}`)
			return err
		}},
	}
	for _, tc := range badCalls {
		id := c.NewCall()
		if err := tc.fn(id); err == nil {
			t.Fatalf("%s unexpectedly succeeded", tc.name)
		}
	}

	// Verify zero leaked call reservations after cancel and invalid calls.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cancels) != 0 || len(c.reserved) != 0 {
		t.Fatalf("leaked call reservations: cancels=%d reserved=%d", len(c.cancels), len(c.reserved))
	}
}

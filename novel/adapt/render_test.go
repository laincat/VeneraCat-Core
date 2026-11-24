package adapt_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/novel/adapt"
)

// Minimal valid 1x1 PNG bytes for offline image tests.
var samplePNG = []byte{
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

type mockImageGen struct {
	mu      sync.Mutex
	calls   int
	reqs    []adapt.PanelImageRequest
	handler func(call int, req adapt.PanelImageRequest) (*adapt.PanelImageResult, error)
}

func (m *mockImageGen) GenerateImage(_ context.Context, req adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	return m.handler(call, req)
}

type httpClientTransport struct {
	client *http.Client
}

func (t *httpClientTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
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

func sampleProjectForRender() *adapt.Project {
	return &adapt.Project{
		ID:    "proj-render-1",
		Title: "Rainy Night Chronicle",
		StyleRef: &adapt.StyleRef{
			ComicTitle: "Personal Ink Sketchbook",
			StyleNotes: "clean ink outlines, soft watercolor wash",
		},
		Characters: adapt.CharacterSheet{
			Characters: []adapt.Character{
				{
					ID:         "char_wenqing",
					Name:       "Wen Qing",
					Aliases:    []string{"Qingqing"},
					Appearance: "long black hair, dark eyes",
					Outfit:     "beige trench coat",
					Traits:     []string{"calm", "sharp"},
				},
			},
		},
		Storyboards: map[string]*adapt.Storyboard{
			"ch1": {
				ChapterID:    "ch1",
				ChapterTitle: "Chapter 1",
				Scenes: []adapt.Scene{
					{
						ID:      "s1",
						Setting: "Rainy apartment hallway",
						Mood:    "suspenseful",
						Panels: []adapt.Panel{
							{
								ID:          "p1",
								Shot:        "wide shot",
								Action:      "Wen Qing pauses by the doorway",
								Characters:  []string{"Qingqing"},
								Prompt:      "halftone rainy doorway silhouette",
								ImageStatus: adapt.ImagePending,
							},
							{
								ID:          "p2",
								Shot:        "close-up",
								Action:      "Wen Qing raises a brass key",
								Characters:  []string{"Wen Qing"},
								Prompt:      "close portrait holding brass key",
								ImageStatus: adapt.ImagePending,
							},
						},
					},
					{
						ID:      "s2",
						Setting: "Dim living room",
						Mood:    "quiet",
						Panels: []adapt.Panel{
							{
								ID:          "p3",
								Shot:        "medium shot",
								Action:      "Light spills across wooden floor",
								Characters:  []string{"char_wenqing"},
								Prompt:      "warm hallway light spilling indoors",
								ImageStatus: adapt.ImagePending,
							},
						},
					},
				},
			},
		},
	}
}

func TestGeminiImageGeneratorRequestAndResponse(t *testing.T) {
	const secretKey = "AIza-super-secret-gemini-key-999"
	encodedPNG := base64.StdEncoding.EncodeToString(samplePNG)

	var capturedHeader http.Header
	var capturedPath string
	var capturedQuery string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"candidates": [{
				"finishReason": "STOP",
				"content": {
					"parts": [
						{"text": "rendered panel"},
						{"inlineData": {"mimeType": "image/png", "data": %q}}
					]
				}
			}],
			"usageMetadata": {
				"promptTokenCount": 320,
				"candidatesTokenCount": 1290,
				"totalTokenCount": 1610
			}
		}`, encodedPNG)
	}))
	defer srv.Close()

	gen := &adapt.GeminiImageGenerator{
		BaseURL:   srv.URL + "/v1beta",
		APIKey:    secretKey,
		Model:     "models/gemini-2.5-flash-image",
		Partition: "ai:gemini",
		Transport: &httpClientTransport{client: srv.Client()},
	}

	req := adapt.PanelImageRequest{
		ProjectID:   "proj-1",
		ChapterID:   "ch1",
		SceneID:     "s1",
		PanelID:     "p1",
		Prompt:      "comic panel of a rainy alley",
		AspectRatio: "3:4",
		References: []adapt.ImageRef{
			{
				ID:       "style-1",
				Kind:     adapt.ImageRefStyle,
				Label:    "Watercolor Sketch",
				MIMEType: "image/png",
				Data:     samplePNG,
			},
			{
				ID:       "char-1",
				Kind:     adapt.ImageRefCharacter,
				Label:    "Wen Qing",
				MIMEType: "image/png",
				Data:     samplePNG,
			},
		},
	}

	res, err := gen.GenerateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if capturedPath != "/v1beta/models/gemini-2.5-flash-image:generateContent" {
		t.Fatalf("unexpected path: %q", capturedPath)
	}
	if capturedQuery != "" {
		t.Fatalf("API key must not be in query string, got %q", capturedQuery)
	}
	if gotKey := capturedHeader.Get("x-goog-api-key"); gotKey != secretKey {
		t.Fatalf("missing x-goog-api-key header")
	}

	// Verify responseModalities and inlineData reference parts.
	genCfg, _ := capturedBody["generationConfig"].(map[string]any)
	mods, _ := genCfg["responseModalities"].([]any)
	if len(mods) != 1 || mods[0] != "IMAGE" {
		t.Fatalf("unexpected responseModalities: %#v", mods)
	}
	imgCfg, _ := genCfg["imageConfig"].(map[string]any)
	if imgCfg["aspectRatio"] != "3:4" {
		t.Fatalf("expected aspectRatio 3:4, got %#v", imgCfg)
	}
	contents, _ := capturedBody["contents"].([]any)
	firstContent, _ := contents[0].(map[string]any)
	parts, _ := firstContent["parts"].([]any)
	inlineCount := 0
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if _, ok := pm["inlineData"]; ok {
			inlineCount++
		}
	}
	if inlineCount != 2 {
		t.Fatalf("expected 2 inlineData reference parts, got %d", inlineCount)
	}

	if res.MIMEType != "image/png" || !bytes.Equal(res.Data, samplePNG) {
		t.Fatalf("unexpected image result: mime=%q len=%d", res.MIMEType, len(res.Data))
	}
	if res.RevisedPrompt != "rendered panel" {
		t.Fatalf("unexpected revised prompt: %q", res.RevisedPrompt)
	}
	if res.Usage.ImageCalls != 1 || res.Usage.InputTokens != 320 || res.Usage.OutputTokens != 1290 || res.Usage.TotalTokens != 1610 {
		t.Fatalf("unexpected usage: %+v", res.Usage)
	}
}

func TestGeminiImageGeneratorSafetyAndRedaction(t *testing.T) {
	const secretKey = "AIza-SECRET-DO-NOT-LEAK-12345"

	t.Run("image-safety-finish-reason", func(t *testing.T) {
		tr := &fakeTransport{
			resp: &host.Response{
				Status: 200,
				Body:   []byte(`{"candidates":[{"finishReason":"IMAGE_SAFETY","finishMessage":"blocked for safety policy"}]}`),
			},
		}
		gen := &adapt.GeminiImageGenerator{
			APIKey:    secretKey,
			Model:     "gemini-2.5-flash-image",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "test"})
		if !errors.Is(err, adapt.ErrContentBlocked) {
			t.Fatalf("expected ErrContentBlocked, got %v", err)
		}
	})

	t.Run("prompt-feedback-blocked", func(t *testing.T) {
		tr := &fakeTransport{
			resp: &host.Response{
				Status: 200,
				Body:   []byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"disallowed"}}`),
			},
		}
		gen := &adapt.GeminiImageGenerator{
			APIKey:    secretKey,
			Model:     "gemini-2.5-flash-image",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "test"})
		if !errors.Is(err, adapt.ErrContentBlocked) {
			t.Fatalf("expected ErrContentBlocked, got %v", err)
		}
	})

	t.Run("snake-case-inline-data", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString(samplePNG)
		res, err := adapt.ParseGeminiImageResponse(200, []byte(`{"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"`+encoded+`"}}]}}]}`))
		if err != nil {
			t.Fatalf("ParseGeminiImageResponse snake_case failed: %v", err)
		}
		if !bytes.Equal(res.Data, samplePNG) || res.MIMEType != "image/png" {
			t.Fatalf("unexpected decoded snake_case image")
		}
	})

	t.Run("transient-error-redacts-api-key", func(t *testing.T) {
		tr := &fakeTransport{
			resp: &host.Response{
				Status: 429,
				Body:   []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota exceeded for key ` + secretKey + `"}}`),
			},
		}
		gen := &adapt.GeminiImageGenerator{
			APIKey:    secretKey,
			Model:     "gemini-2.5-flash-image",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "test"})
		if err == nil {
			t.Fatal("expected error")
		}
		if errors.Is(err, adapt.ErrContentBlocked) {
			t.Fatalf("429 should be transient, not ErrContentBlocked: %v", err)
		}
		if strings.Contains(err.Error(), secretKey) {
			t.Fatalf("API key leaked into error: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("expected [REDACTED] placeholder in error: %v", err)
		}
	})
}

func TestOpenAIImageGeneratorGenerationsAndEdits(t *testing.T) {
	const secretKey = "sk-openai-secret-key-do-not-leak"
	encodedPNG := base64.StdEncoding.EncodeToString(samplePNG)

	t.Run("generations-json", func(t *testing.T) {
		var gotAuth string
		var gotPath string
		var gotBody map[string]any

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"output_format": "png",
				"data": [{"b64_json": %q, "revised_prompt": "polished panel"}],
				"usage": {"input_tokens": 90, "output_tokens": 210, "total_tokens": 300}
			}`, encodedPNG)
		}))
		defer srv.Close()

		gen := &adapt.OpenAIImageGenerator{
			BaseURL:   srv.URL + "/v1",
			APIKey:    secretKey,
			Model:     "dall-e-3",
			Size:      "1024x1024",
			Quality:   "hd",
			Transport: &httpClientTransport{client: srv.Client()},
		}
		res, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{
			Prompt: "rainy comic panel",
		})
		if err != nil {
			t.Fatalf("GenerateImage: %v", err)
		}
		if gotPath != "/v1/images/generations" {
			t.Fatalf("unexpected path: %q", gotPath)
		}
		if gotAuth != "Bearer "+secretKey {
			t.Fatalf("unexpected Authorization header")
		}
		if gotBody["response_format"] != "b64_json" {
			t.Fatalf("expected default response_format b64_json for dall-e-3, got %#v", gotBody["response_format"])
		}
		if res.MIMEType != "image/png" || !bytes.Equal(res.Data, samplePNG) {
			t.Fatalf("unexpected image result")
		}
		if res.Usage.ImageCalls != 1 || res.Usage.TotalTokens != 300 {
			t.Fatalf("unexpected usage: %+v", res.Usage)
		}
	})

	t.Run("gpt-image-omits-response-format-and-uses-edits-with-refs", func(t *testing.T) {
		var gotPath string
		var gotContentType string
		var gotModel string
		var fileFieldNames []string
		var fileLengths []int

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotContentType = r.Header.Get("Content-Type")
			_, params, err := mime.ParseMediaType(gotContentType)
			if err != nil {
				t.Errorf("ParseMediaType: %v", err)
				w.WriteHeader(400)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("NextPart: %v", err)
					break
				}
				data, _ := io.ReadAll(p)
				switch p.FormName() {
				case "model":
					gotModel = string(data)
				case "image", "image[]":
					fileFieldNames = append(fileFieldNames, p.FormName())
					fileLengths = append(fileLengths, len(data))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, encodedPNG)
		}))
		defer srv.Close()

		gen := &adapt.OpenAIImageGenerator{
			BaseURL:      srv.URL + "/v1",
			APIKey:       secretKey,
			Model:        "gpt-image-1",
			OutputFormat: "png",
			Transport:    &httpClientTransport{client: srv.Client()},
		}
		res, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{
			Prompt: "draw Wen Qing in this style",
			References: []adapt.ImageRef{
				{Kind: adapt.ImageRefStyle, MIMEType: "image/png", Data: samplePNG},
				{Kind: adapt.ImageRefCharacter, MIMEType: "image/png", Data: samplePNG},
			},
		})
		if err != nil {
			t.Fatalf("GenerateImage edits: %v", err)
		}
		if gotPath != "/v1/images/edits" {
			t.Fatalf("expected /v1/images/edits, got %q", gotPath)
		}
		if gotModel != "gpt-image-1" || len(fileFieldNames) != 2 || fileFieldNames[0] != "image[]" {
			t.Fatalf("unexpected multipart fields: model=%q files=%v lens=%v", gotModel, fileFieldNames, fileLengths)
		}
		if !bytes.Equal(res.Data, samplePNG) || res.MIMEType != "image/png" {
			t.Fatalf("unexpected decoded edits image")
		}
	})

	t.Run("policy-violation-and-redaction", func(t *testing.T) {
		tr := &fakeTransport{
			resp: &host.Response{
				Status: 400,
				Body:   []byte(`{"error":{"code":"content_policy_violation","type":"image_generation_user_error","message":"rejected for key ` + secretKey + `"}}`),
			},
		}
		gen := &adapt.OpenAIImageGenerator{
			APIKey:    secretKey,
			Model:     "gpt-image-1",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "blocked"})
		if !errors.Is(err, adapt.ErrContentBlocked) {
			t.Fatalf("expected ErrContentBlocked, got %v", err)
		}
		if strings.Contains(err.Error(), secretKey) {
			t.Fatalf("OpenAI API key leaked in error: %v", err)
		}
	})
}

func TestDeepSeekAndCodexResponseShapes(t *testing.T) {
	ctx := context.Background()
	storyboardJSON := `{"scenes":[{"id":"s1","setting":"Rainy street","panels":[{"id":"p1","shot":"wide","action":"Walks in rain","prompt":"rainy street"}]}]}`

	t.Run("deepseek-reasoning-content-and-fenced-json", func(t *testing.T) {
		body := `{
			"choices": [{
				"message": {
					"role": "assistant",
					"reasoning_content": "Let me break this chapter down into scenes and panels...",
					"content": ` + mustJSON("```json\n"+storyboardJSON+"\n```") + `
				}
			}],
			"usage": {"prompt_tokens": 210, "completion_tokens": 140, "total_tokens": 350}
		}`
		tr := &fakeTransport{resp: &host.Response{Status: 200, Body: []byte(body)}}
		temp := 0.2
		gen := &adapt.AIAPIGenerator{
			Config: aiapi.Config{
				Protocol: aiapi.Chat,
				BaseURL:  "https://api.deepseek.com/v1",
				APIKey:   "sk-deepseek-test",
			},
			Model:       "deepseek-reasoner",
			MaxTokens:   2048,
			Temperature: &temp,
			JSONMode:    true,
			Transport:   tr,
		}
		resp, err := gen.Generate(ctx, adapt.GenerateRequest{System: "sys", Prompt: "excerpt"})
		if err != nil {
			t.Fatalf("DeepSeek Generate: %v", err)
		}
		var sent map[string]any
		if err := json.Unmarshal(tr.lastReq.Body, &sent); err != nil {
			t.Fatalf("decode sent body: %v", err)
		}
		if sent["max_tokens"] != float64(2048) {
			t.Fatalf("expected max_tokens=2048, got %#v", sent["max_tokens"])
		}
		rf, _ := sent["response_format"].(map[string]any)
		if rf["type"] != "json_object" {
			t.Fatalf("expected json_object response_format, got %#v", sent["response_format"])
		}
		parsed, err := adapt.ParseAdaptationJSON(resp.Text)
		if err != nil {
			t.Fatalf("ParseAdaptationJSON: %v", err)
		}
		if len(parsed.Scenes) != 1 || parsed.Scenes[0].Panels[0].Shot != "wide" {
			t.Fatalf("unexpected parsed storyboard: %+v", parsed)
		}
	})

	t.Run("deepseek-think-tag-plus-fence", func(t *testing.T) {
		raw := "<think>internal chain of thought</think>\n```json\n" + storyboardJSON + "\n```"
		parsed, err := adapt.ParseAdaptationJSON(raw)
		if err != nil {
			t.Fatalf("ParseAdaptationJSON with <think>: %v", err)
		}
		if len(parsed.Scenes) != 1 {
			t.Fatalf("unexpected scenes: %+v", parsed.Scenes)
		}
	})

	t.Run("deepseek-empty-content-with-reasoning-only-fails", func(t *testing.T) {
		body := `{"choices":[{"message":{"reasoning_content":"still thinking","content":""}}]}`
		if _, err := adapt.ParseAIAPIResponse(aiapi.Chat, 200, []byte(body)); err == nil {
			t.Fatal("expected error when chat content is empty")
		}
	})

	t.Run("codex-responses-multi-segment-output-skips-reasoning", func(t *testing.T) {
		body := `{
			"id": "resp_codex_1",
			"status": "completed",
			"output": [
				{
					"id": "rs_1",
					"type": "reasoning",
					"content": [{"type": "reasoning_text", "text": "NOT JSON REASONING THAT MUST BE IGNORED"}]
				},
				{
					"id": "msg_1",
					"type": "message",
					"content": [
						{"type": "output_text", "text": "{\"scenes\":[{\"id\":\"s1\",\"setting\":\"Station\","},
						{"type": "output_text", "text": "\"panels\":[{\"id\":\"p1\",\"shot\":\"medium\",\"action\":\"Board train\",\"prompt\":\"night train station\"}]}]}"}
					]
				}
			],
			"usage": {"input_tokens": 180, "output_tokens": 95, "total_tokens": 275}
		}`
		tr := &fakeTransport{resp: &host.Response{Status: 200, Body: []byte(body)}}
		gen := &adapt.AIAPIGenerator{
			Config: aiapi.Config{
				Protocol: aiapi.Responses,
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "sk-codex-test",
			},
			Model:           "gpt-5-codex",
			MaxTokens:       3000,
			ReasoningEffort: "medium",
			JSONMode:        true,
			Transport:       tr,
		}
		resp, err := gen.Generate(ctx, adapt.GenerateRequest{System: "sys", Prompt: "excerpt"})
		if err != nil {
			t.Fatalf("Codex Generate: %v", err)
		}
		var sent map[string]any
		_ = json.Unmarshal(tr.lastReq.Body, &sent)
		if sent["max_output_tokens"] != float64(3000) {
			t.Fatalf("expected max_output_tokens=3000, got %#v", sent["max_output_tokens"])
		}
		reasoning, _ := sent["reasoning"].(map[string]any)
		if reasoning["effort"] != "medium" {
			t.Fatalf("expected reasoning.effort=medium, got %#v", sent["reasoning"])
		}
		parsed, err := adapt.ParseAdaptationJSON(resp.Text)
		if err != nil {
			t.Fatalf("ParseAdaptationJSON from multi-segment responses: %v (raw=%q)", err, resp.Text)
		}
		if parsed.Scenes[0].Setting != "Station" {
			t.Fatalf("unexpected scene setting: %+v", parsed.Scenes[0])
		}
	})

	t.Run("codex-responses-refusal", func(t *testing.T) {
		body := `{"output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot comply"}]}]}`
		_, err := adapt.ParseAIAPIResponse(aiapi.Responses, 200, []byte(body))
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("expected refusal error, got %v", err)
		}
	})
}

func TestRenderStoryboardBudgetResumeAndFeedback(t *testing.T) {
	proj := sampleProjectForRender()
	proj.Budget = adapt.Budget{MaxImageCalls: 2}

	var transitions []string
	saved := map[string][]byte{}
	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, mimeType string, data []byte) (string, error) {
		key := fmt.Sprintf("%s/%s/%s", meta.ChapterID, meta.SceneID, meta.PanelID)
		saved[key] = bytes.Clone(data)
		return "ref://" + key, nil
	})

	gen := &mockImageGen{
		handler: func(call int, req adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
			// Verify prompt carries character sheet appearance & style notes.
			if !strings.Contains(req.Prompt, "long black hair") || !strings.Contains(req.Prompt, "soft watercolor wash") {
				t.Errorf("call %d prompt missing character/style details: %q", call, req.Prompt)
			}
			// Panel 1 has 1 style ref; Panel 2 should also receive panel 1's feedback ref for Wen Qing!
			if req.PanelID == "p1" && len(req.References) != 1 {
				t.Errorf("p1 expected 1 style reference, got %d", len(req.References))
			}
			if req.PanelID == "p2" && len(req.References) != 2 {
				t.Errorf("p2 expected style + feedback character reference (2), got %d", len(req.References))
			}
			return &adapt.PanelImageResult{
				MIMEType: "image/png",
				Data:     append(bytes.Clone(samplePNG), byte(call)),
				Usage:    adapt.Usage{ImageCalls: 1, InputTokens: 100, OutputTokens: 200, TotalTokens: 300},
			}, nil
		},
	}

	opts := adapt.RenderOptions{
		StyleReferences: []adapt.ImageRef{
			{ID: "style-ref-1", Kind: adapt.ImageRefStyle, Label: "Ink Wash", MIMEType: "image/png", Data: samplePNG},
		},
		FeedbackCharacterRefs: true,
		OnStatusChange: func(sceneID, panelID string, status adapt.ImageStatus, _ error) {
			transitions = append(transitions, fmt.Sprintf("%s:%s=%s", sceneID, panelID, status))
		},
	}

	// First run hits MaxImageCalls=2 before panel 3.
	res1, err := adapt.RenderStoryboard(context.Background(), proj, "ch1", gen, sink, opts)
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if res1.Rendered != 2 || proj.Usage.ImageCalls != 2 || gen.calls != 2 {
		t.Fatalf("unexpected first run state: res=%+v usage=%+v calls=%d", res1, proj.Usage, gen.calls)
	}
	// Panel 3 must stay pending so resuming picks it up cleanly.
	if st := proj.Storyboards["ch1"].Scenes[1].Panels[0].ImageStatus; st != adapt.ImagePending {
		t.Fatalf("panel 3 should remain pending on pre-call budget stop, got %q", st)
	}
	wantTransitions := []string{
		"s1:p1=generating", "s1:p1=done",
		"s1:p2=generating", "s1:p2=done",
	}
	if strings.Join(transitions, ",") != strings.Join(wantTransitions, ",") {
		t.Fatalf("unexpected status transitions: %v", transitions)
	}

	// Raise budget and resume: panels 1 & 2 must be skipped, only panel 3 rendered.
	proj.Budget.MaxImageCalls = 3
	res2, err := adapt.RenderStoryboard(context.Background(), proj, "ch1", gen, sink, opts)
	if err != nil {
		t.Fatalf("resumed RenderStoryboard: %v", err)
	}
	if res2.Skipped != 2 || res2.Rendered != 1 || proj.Usage.ImageCalls != 3 || gen.calls != 3 {
		t.Fatalf("unexpected resume result: res=%+v usage=%+v calls=%d", res2, proj.Usage, gen.calls)
	}
	if len(saved) != 3 {
		t.Fatalf("expected 3 saved images, got %d", len(saved))
	}

	// Project JSON backward-compatible round-trip check.
	blob, err := json.Marshal(proj)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	var roundTrip adapt.Project
	if err := json.Unmarshal(blob, &roundTrip); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}
	if roundTrip.Usage.ImageCalls != 3 || roundTrip.Budget.MaxImageCalls != 3 {
		t.Fatalf("round-trip budget/usage mismatch: %+v %+v", roundTrip.Budget, roundTrip.Usage)
	}
	if roundTrip.Storyboards["ch1"].Scenes[1].Panels[0].ImageRefID != "ref://ch1/s2/p3" {
		t.Fatalf("round-trip panel ImageRefID mismatch: %+v", roundTrip.Storyboards["ch1"].Scenes[1].Panels[0])
	}
}

func TestRenderPanelBlockedNeverRetriesOrBurnsBudget(t *testing.T) {
	proj := sampleProjectForRender()
	proj.Budget = adapt.Budget{MaxImageCalls: 5}

	gen := &mockImageGen{
		handler: func(call int, req adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
			if req.PanelID == "p1" {
				return nil, fmt.Errorf("%w: gemini finishReason IMAGE_SAFETY", adapt.ErrContentBlocked)
			}
			return &adapt.PanelImageResult{
				MIMEType: "image/png",
				Data:     samplePNG,
				Usage:    adapt.Usage{ImageCalls: 1},
			}, nil
		},
	}

	res, err := adapt.RenderStoryboard(context.Background(), proj, "ch1", gen, nil, adapt.RenderOptions{
		MaxRetries:      3,
		ContinueOnError: true,
	})
	if err != nil {
		t.Fatalf("RenderStoryboard with ContinueOnError: %v", err)
	}
	if res.Blocked != 1 || res.Failed != 1 || res.Rendered != 2 {
		t.Fatalf("unexpected render counts: %+v", res)
	}
	// p1 must have been called exactly once (no retries), p2 once, p3 once -> total 3 calls.
	if gen.calls != 3 {
		t.Fatalf("blocked panel must not retry: expected 3 total calls, got %d", gen.calls)
	}
	// Budget must only count the 2 rendered panels, not the blocked one.
	if proj.Usage.ImageCalls != 2 {
		t.Fatalf("blocked panel must not burn image budget: expected 2 ImageCalls, got %d", proj.Usage.ImageCalls)
	}
	p1 := proj.Storyboards["ch1"].Scenes[0].Panels[0]
	if p1.ImageStatus != adapt.ImageFailed || !strings.Contains(p1.ImageError, "IMAGE_SAFETY") {
		t.Fatalf("unexpected blocked panel state: %+v", p1)
	}

	// With ContinueOnError: false, a blocked panel stops the run immediately.
	proj2 := sampleProjectForRender()
	gen2 := &mockImageGen{
		handler: func(_ int, _ adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
			return nil, fmt.Errorf("%w: gemini finishReason IMAGE_SAFETY", adapt.ErrContentBlocked)
		},
	}
	res2, err := adapt.RenderStoryboard(context.Background(), proj2, "ch1", gen2, nil, adapt.RenderOptions{
		MaxRetries:      2,
		ContinueOnError: false,
	})
	if !errors.Is(err, adapt.ErrContentBlocked) {
		t.Fatalf("expected ErrContentBlocked fail-fast, got %v", err)
	}
	if gen2.calls != 1 || res2.Blocked != 1 || proj2.Usage.ImageCalls != 0 {
		t.Fatalf("unexpected fail-fast blocked state: calls=%d res=%+v usage=%+v", gen2.calls, res2, proj2.Usage)
	}
	if proj2.Storyboards["ch1"].Scenes[0].Panels[1].ImageStatus != adapt.ImagePending {
		t.Fatalf("panel 2 should remain pending after fail-fast stop")
	}
}

func TestRenderPanelSinkFailureStillChargesUsageAndMarksFailed(t *testing.T) {
	proj := sampleProjectForRender()
	gen := &mockImageGen{
		handler: func(_ int, _ adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
			return &adapt.PanelImageResult{
				MIMEType: "image/png",
				Data:     samplePNG,
				Usage:    adapt.Usage{ImageCalls: 1, InputTokens: 40, OutputTokens: 60},
			}, nil
		},
	}
	sinkErr := errors.New("disk quota exceeded")
	sink := adapt.ImageSinkFunc(func(_ context.Context, _ adapt.PanelImageMeta, _ string, _ []byte) (string, error) {
		return "", sinkErr
	})
	_, err := adapt.RenderPanel(context.Background(), proj, "ch1", "s1", "p1", gen, sink, adapt.RenderOptions{})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("expected sink error, got %v", err)
	}
	if proj.Usage.ImageCalls != 1 || proj.Usage.TotalTokens != 100 {
		t.Fatalf("API call usage should still be recorded when sink fails: %+v", proj.Usage)
	}
	if proj.Storyboards["ch1"].Scenes[0].Panels[0].ImageStatus != adapt.ImageFailed {
		t.Fatalf("panel should be marked failed when sink fails")
	}
}

func TestAIAPIGeneratorRedactsSecretOnError(t *testing.T) {
	const secret = "sk-deepseek-secret-never-log"
	tr := &fakeTransport{
		resp: &host.Response{
			Status: 401,
			Body:   []byte("invalid token: " + secret),
		},
	}
	gen := &adapt.AIAPIGenerator{
		Config: aiapi.Config{
			Protocol: aiapi.Chat,
			BaseURL:  "https://api.deepseek.com/v1",
			APIKey:   secret,
		},
		Model:     "deepseek-chat",
		Transport: tr,
	}
	_, err := gen.Generate(context.Background(), adapt.GenerateRequest{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected HTTP 401 error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("API key leaked into AIAPIGenerator error: %v", err)
	}
}

func TestRenderPanelTransientRetryAndSinglePanelRetry(t *testing.T) {
	proj := sampleProjectForRender()
	var attempts atomic.Int32

	gen := &mockImageGen{
		handler: func(_ int, _ adapt.PanelImageRequest) (*adapt.PanelImageResult, error) {
			n := attempts.Add(1)
			if n <= 2 {
				return nil, errors.New("temporary 503 service unavailable")
			}
			return &adapt.PanelImageResult{
				MIMEType: "image/png",
				Data:     samplePNG,
				Usage:    adapt.Usage{ImageCalls: 1},
			}, nil
		},
	}

	// MaxRetries=1 allows 2 attempts total -> both fail -> panel marked failed.
	if _, err := adapt.RenderPanel(context.Background(), proj, "ch1", "s1", "p1", gen, nil, adapt.RenderOptions{MaxRetries: 1}); err == nil {
		t.Fatal("expected failure after 2 transient errors")
	}
	if proj.Storyboards["ch1"].Scenes[0].Panels[0].ImageStatus != adapt.ImageFailed {
		t.Fatalf("expected p1 to be failed")
	}
	if proj.Usage.ImageCalls != 0 {
		t.Fatalf("failed transient attempts must not increment ImageCalls, got %d", proj.Usage.ImageCalls)
	}

	// Single-panel retry succeeds on attempt 3.
	res, err := adapt.RenderPanel(context.Background(), proj, "ch1", "s1", "p1", gen, nil, adapt.RenderOptions{})
	if err != nil {
		t.Fatalf("single-panel retry failed: %v", err)
	}
	if res.Skipped || proj.Storyboards["ch1"].Scenes[0].Panels[0].ImageStatus != adapt.ImageDone {
		t.Fatalf("expected p1 done after retry: res=%+v panel=%+v", res, proj.Storyboards["ch1"].Scenes[0].Panels[0])
	}

	// Calling RenderPanel again without ForceRerender skips.
	skipped, err := adapt.RenderPanel(context.Background(), proj, "ch1", "s1", "p1", gen, nil, adapt.RenderOptions{})
	if err != nil || !skipped.Skipped {
		t.Fatalf("expected skip on done panel: res=%+v err=%v", skipped, err)
	}
	if proj.Usage.ImageCalls != 1 {
		t.Fatalf("expected 1 ImageCall after skip, got %d", proj.Usage.ImageCalls)
	}

	// ForceRerender re-runs the done panel.
	rerendered, err := adapt.RenderPanel(context.Background(), proj, "ch1", "s1", "p1", gen, nil, adapt.RenderOptions{ForceRerender: true})
	if err != nil || rerendered.Skipped || proj.Usage.ImageCalls != 2 {
		t.Fatalf("expected ForceRerender to render again: res=%+v usage=%+v err=%v", rerendered, proj.Usage, err)
	}
}

func TestLegacyProjectJSONBackwardCompatibility(t *testing.T) {
	// Wave 4B-1 serialized Project JSON without MaxImageCalls / ImageCalls / ImageRefID.
	legacy := `{
		"id": "legacy-proj",
		"title": "Legacy Novel",
		"characters": {"characters": []},
		"storyboards": {
			"c1": {
				"chapterId": "c1",
				"scenes": [{
					"id": "s1",
					"setting": "Room",
					"panels": [{
						"id": "p1",
						"shot": "wide",
						"action": "sit",
						"prompt": "quiet room",
						"imageStatus": "pending"
					}]
				}]
			}
		},
		"budget": {"maxCalls": 5, "maxTotalTokens": 10000},
		"usage": {"calls": 1, "inputTokens": 100, "outputTokens": 50, "totalTokens": 150}
	}`
	var proj adapt.Project
	if err := json.Unmarshal([]byte(legacy), &proj); err != nil {
		t.Fatalf("unmarshal legacy project: %v", err)
	}
	if proj.Budget.MaxCalls != 5 || proj.Budget.MaxImageCalls != 0 || proj.Usage.ImageCalls != 0 {
		t.Fatalf("unexpected decoded legacy budget/usage: %+v %+v", proj.Budget, proj.Usage)
	}
	if err := proj.Usage.CheckNextImageCall(proj.Budget); err != nil {
		t.Fatalf("unlimited MaxImageCalls should pass: %v", err)
	}
}

func TestConcurrentGeneratorsAndStoryboardsRaceFree(t *testing.T) {
	encodedPNG := base64.StdEncoding.EncodeToString(samplePNG)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, ":generateContent"):
			_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30}}`, encodedPNG)
		case strings.HasSuffix(r.URL.Path, "/images/generations"), strings.HasSuffix(r.URL.Path, "/images/edits"):
			_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`, encodedPNG)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	tr := &httpClientTransport{client: srv.Client()}
	geminiGen := &adapt.GeminiImageGenerator{
		BaseURL:   srv.URL + "/v1beta",
		APIKey:    "gemini-race-key",
		Model:     "gemini-2.5-flash-image",
		Transport: tr,
	}
	openaiGen := &adapt.OpenAIImageGenerator{
		BaseURL:   srv.URL + "/v1",
		APIKey:    "openai-race-key",
		Model:     "gpt-image-1",
		Transport: tr,
	}

	sharedStyle := []adapt.ImageRef{
		{ID: "shared-style", Kind: adapt.ImageRefStyle, MIMEType: "image/png", Data: samplePNG},
	}
	sharedChars := map[string][]adapt.ImageRef{
		"Wen Qing": {{ID: "shared-char", Kind: adapt.ImageRefCharacter, MIMEType: "image/png", Data: samplePNG}},
	}

	var savedCount atomic.Int64
	sink := adapt.ImageSinkFunc(func(_ context.Context, meta adapt.PanelImageMeta, _ string, _ []byte) (string, error) {
		n := savedCount.Add(1)
		return fmt.Sprintf("mem://%s/%s/%d", meta.ProjectID, meta.PanelID, n), nil
	})

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			proj := sampleProjectForRender()
			proj.ID = fmt.Sprintf("proj-worker-%d", idx)
			var gen adapt.ImageGenerator = geminiGen
			if idx%2 == 1 {
				gen = openaiGen
			}
			res, err := adapt.RenderStoryboard(context.Background(), proj, "ch1", gen, sink, adapt.RenderOptions{
				StyleReferences:       sharedStyle,
				CharacterReferences:   sharedChars,
				FeedbackCharacterRefs: true,
			})
			if err != nil {
				errCh <- fmt.Errorf("worker %d: %w", idx, err)
				return
			}
			if res.Rendered != 3 {
				errCh <- fmt.Errorf("worker %d: expected 3 rendered panels, got %d", idx, res.Rendered)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if savedCount.Load() != workers*3 {
		t.Fatalf("expected %d saved images, got %d", workers*3, savedCount.Load())
	}
}

func TestRedactPreservesContextCancelAndTrimmedKey(t *testing.T) {
	const rawKey = "AIza-trimmed-secret-key"

	t.Run("trimmed-key-redacted", func(t *testing.T) {
		tr := &fakeTransport{
			resp: &host.Response{
				Status: 401,
				Body:   []byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"bad key ` + rawKey + `"}}`),
			},
		}
		gen := &adapt.GeminiImageGenerator{
			APIKey:    "  " + rawKey + "  ",
			Model:     "gemini-2.5-flash-image",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "test"})
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), rawKey) {
			t.Fatalf("trimmed API key leaked into error: %v", err)
		}
	})

	t.Run("context-canceled-preserved-after-redaction", func(t *testing.T) {
		tr := &fakeTransport{
			err: fmt.Errorf("request with key %s aborted: %w", rawKey, context.Canceled),
		}
		gen := &adapt.GeminiImageGenerator{
			APIKey:    rawKey,
			Model:     "gemini-2.5-flash-image",
			Transport: tr,
		}
		_, err := gen.GenerateImage(context.Background(), adapt.PanelImageRequest{Prompt: "test"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled to survive redaction, got %v", err)
		}
		if strings.Contains(err.Error(), rawKey) {
			t.Fatalf("API key leaked into canceled error: %v", err)
		}
	})

	t.Run("openai-multipart-rejects-crlf-mime", func(t *testing.T) {
		gen := &adapt.OpenAIImageGenerator{
			APIKey: "sk-test-key",
			Model:  "gpt-image-1",
		}
		_, err := gen.BuildRequest(adapt.PanelImageRequest{
			Prompt: "test",
			References: []adapt.ImageRef{
				{Kind: adapt.ImageRefStyle, MIMEType: "image/png\r\nX-Injected: bad", Data: samplePNG},
			},
		})
		if err == nil {
			t.Fatal("expected invalid multipart MIME type to be rejected")
		}
	})
}

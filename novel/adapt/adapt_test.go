package adapt_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/novel"
	"github.com/laincat/veneracat-core/novel/adapt"
)

type mockGen struct {
	calls   int
	reqs    []adapt.GenerateRequest
	handler func(call int, req adapt.GenerateRequest) (*adapt.GenerateResponse, error)
}

func (m *mockGen) Generate(_ context.Context, req adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
	m.calls++
	m.reqs = append(m.reqs, req)
	return m.handler(m.calls, req)
}

type fakeTransport struct {
	lastReq *host.Request
	resp    *host.Response
	err     error
}

func (f *fakeTransport) Do(_ context.Context, req *host.Request) (*host.Response, error) {
	f.lastReq = req
	return f.resp, f.err
}

func TestExtractAndChunkHTML(t *testing.T) {
	raw := `<div id="txt"><script>alert(1)</script><style>.ad{display:none}</style>
	<p>　　第一段：夜雨敲窗，少年推门而出。</p>
	<p>第二段：街灯昏黄，远处传来脚步声。<br/>他停下了脚步。</p>
	</div>`
	plain := adapt.ExtractText(raw)
	if strings.Contains(plain, "alert") || strings.Contains(plain, "display:none") {
		t.Fatalf("script/style leaked into plain text: %q", plain)
	}
	paras := strings.Split(plain, "\n\n")
	if len(paras) != 3 {
		t.Fatalf("expected 3 paragraphs, got %d: %q", len(paras), plain)
	}

	// Paragraph-boundary chunking.
	chunks := adapt.ChunkText(plain, 25)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for maxRunes=25, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > 25 {
			t.Fatalf("chunk %d exceeded maxRunes: %d > 25 (%q)", i, n, c)
		}
	}

	// Oversized single paragraph splits cleanly at sentence boundaries.
	longPara := "第一句很长很长的话。第二句同样很长很长的话！第三句收尾。"
	parts := adapt.ChunkText(longPara, 15)
	if len(parts) != 3 || parts[0] != "第一句很长很长的话。" || parts[1] != "第二句同样很长很长的话！" {
		t.Fatalf("unexpected sentence split: %#v", parts)
	}
}

func TestAIAPIGeneratorProtocols(t *testing.T) {
	ctx := context.Background()
	sampleOut := `{"scenes":[{"setting":"Rainy street","panels":[{"shot":"wide","action":"Boy steps out","prompt":"rainy night street, anime style"}]}]}`

	cases := []struct {
		name     string
		proto    aiapi.Protocol
		wantPath string
		respBody string
		wantIn   int64
		wantOut  int64
	}{
		{
			name:     "openai-chat",
			proto:    aiapi.Chat,
			wantPath: "/chat/completions",
			respBody: `{"choices":[{"message":{"content":` + mustJSON(sampleOut) + `}}],"usage":{"prompt_tokens":120,"completion_tokens":80,"total_tokens":200}}`,
			wantIn:   120,
			wantOut:  80,
		},
		{
			name:     "openai-responses",
			proto:    aiapi.Responses,
			wantPath: "/responses",
			respBody: `{"output_text":` + mustJSON(sampleOut) + `,"usage":{"input_tokens":150,"output_tokens":90,"total_tokens":240}}`,
			wantIn:   150,
			wantOut:  90,
		},
		{
			name:     "anthropic-messages",
			proto:    aiapi.Messages,
			wantPath: "/messages",
			respBody: `{"content":[{"type":"text","text":` + mustJSON(sampleOut) + `}],"usage":{"input_tokens":110,"output_tokens":70}}`,
			wantIn:   110,
			wantOut:  70,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeTransport{
				resp: &host.Response{
					Status: 200,
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   []byte(tc.respBody),
				},
			}
			gen := &adapt.AIAPIGenerator{
				Config: aiapi.Config{
					Protocol: tc.proto,
					BaseURL:  "https://api.example.test/v1",
					APIKey:   "test-key-only",
				},
				Model:     "test-model",
				MaxTokens: 2048,
				Transport: tr,
			}
			got, err := gen.Generate(ctx, adapt.GenerateRequest{System: "sys", Prompt: "hello"})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if !strings.HasSuffix(tr.lastReq.URL, tc.wantPath) {
				t.Fatalf("unexpected URL %q want suffix %q", tr.lastReq.URL, tc.wantPath)
			}
			if tr.lastReq.Cookies {
				t.Fatal("AI request must not enable cookies")
			}
			if got.Usage.InputTokens != tc.wantIn || got.Usage.OutputTokens != tc.wantOut || got.Usage.TotalTokens != tc.wantIn+tc.wantOut {
				t.Fatalf("unexpected usage: %+v", got.Usage)
			}
			if _, err := adapt.ParseAdaptationJSON(got.Text); err != nil {
				t.Fatalf("ParseAdaptationJSON: %v", err)
			}
		})
	}
}

func TestParseAdaptationJSONValidation(t *testing.T) {
	// Fenced valid JSON succeeds and defaults imageStatus to pending.
	fenced := "```json\n{\"scenes\":[{\"setting\":\"Room\",\"panels\":[{\"shot\":\"close-up\",\"action\":\"Smiles\",\"prompt\":\"smiling portrait\"}]}]}\n```"
	out, err := adapt.ParseAdaptationJSON(fenced)
	if err != nil {
		t.Fatalf("valid fenced JSON failed: %v", err)
	}
	if out.Scenes[0].Panels[0].ImageStatus != adapt.ImagePending {
		t.Fatalf("expected default imageStatus pending, got %q", out.Scenes[0].Panels[0].ImageStatus)
	}

	invalidCases := []struct {
		name string
		raw  string
	}{
		{"malformed", "{not-json"},
		{"empty-scenes", `{"scenes":[]}`},
		{"empty-panels", `{"scenes":[{"setting":"Room","panels":[]}]}`},
		{"missing-prompt", `{"scenes":[{"setting":"Room","panels":[{"shot":"wide","action":"walk"}]}]}`},
		{"bad-status", `{"scenes":[{"setting":"Room","panels":[{"shot":"wide","action":"walk","prompt":"p","imageStatus":"bogus"}]}]}`},
		{"trailing-garbage", `{"scenes":[{"setting":"Room","panels":[{"shot":"wide","action":"walk","prompt":"p"}]}]} extra`},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adapt.ParseAdaptationJSON(tc.raw); !errors.Is(err, adapt.ErrInvalidStoryboard) {
				t.Fatalf("want ErrInvalidStoryboard, got %v", err)
			}
		})
	}
}

func TestMergeCharactersCrossChapter(t *testing.T) {
	sheet := &adapt.CharacterSheet{
		Characters: []adapt.Character{
			{
				ID:             "c_wenqing",
				Name:           "温青",
				Aliases:        []string{"青青"},
				Appearance:     "black hair, dark eyes, slender build",
				Outfit:         "beige trench coat",
				Traits:         []string{"calm"},
				FirstChapterID: "ch1",
			},
		},
	}

	added := adapt.MergeCharacters(sheet, []adapt.Character{
		{
			ID:         "c_wenqing",
			Name:       "温青",
			Aliases:    []string{"小温"},
			Appearance: "blonde hair (should NOT overwrite)",
			Outfit:     "red dress (should NOT overwrite)",
			Traits:     []string{"calm", "determined"},
			Notes:      "holds spare key",
		},
		{
			Name:       "林远",
			Appearance: "tall, short hair",
			Traits:     []string{"protective"},
		},
	}, "ch2")

	if len(added) != 1 || added[0].Name != "林远" || added[0].FirstChapterID != "ch2" {
		t.Fatalf("unexpected added characters: %+v", added)
	}
	if len(sheet.Characters) != 2 {
		t.Fatalf("expected 2 characters, got %d", len(sheet.Characters))
	}
	wen := sheet.Characters[0]
	if wen.Appearance != "black hair, dark eyes, slender build" || wen.Outfit != "beige trench coat" {
		t.Fatalf("existing appearance/outfit was overwritten: %+v", wen)
	}
	if len(wen.Aliases) != 2 || len(wen.Traits) != 2 || !strings.Contains(wen.Notes, "holds spare key") {
		t.Fatalf("aliases/traits/notes merge failed: %+v", wen)
	}
}

func TestBudgetEnforcement(t *testing.T) {
	proj := &adapt.Project{
		ID:     "proj-budget",
		Title:  "Budget Test",
		Budget: adapt.Budget{MaxTotalTokens: 100},
	}
	gen := &mockGen{
		handler: func(call int, _ adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			return &adapt.GenerateResponse{
				Text:  `{"scenes":[{"setting":"Hall","panels":[{"shot":"wide","action":"talk","prompt":"hallway"}]}]}`,
				Usage: adapt.Usage{Calls: 1, InputTokens: 60, OutputTokens: 50, TotalTokens: 110},
			}, nil
		},
	}

	// Two paragraphs forced into two chunks: chunk 1 spends 110 tokens (>= 100),
	// so chunk 2 must be blocked BEFORE calling gen a second time.
	html := "<p>第一段内容足够长以触发切分。</p><p>第二段内容将在超预算时被拦截。</p>"
	_, err := adapt.AdaptChapter(context.Background(), proj, novel.Chapter{ID: "c1", Title: "Ch1"}, novel.ChapterContent{HTML: html}, gen, adapt.AdaptOptions{MaxChunkRunes: 18})
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if gen.calls != 1 {
		t.Fatalf("expected generator to stop after 1 call, got %d calls", gen.calls)
	}
}

func TestAdaptChapterEndToEndRealChineseHTML(t *testing.T) {
	rawHTML, err := os.ReadFile(filepath.Join("testdata", "sample_chapter.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	proj := &adapt.Project{
		ID:             "proj-biquge365-173162",
		NovelSourceKey: "biquge365",
		NovelID:        "https://www.biquge365.net/book/173162/",
		Title:          "龙傲天男主也会怀崽吗",
		StyleRef: &adapt.StyleRef{
			ComicSourceKey:   "local_ref",
			ComicID:          "comic_001",
			ComicTitle:       "Local Watercolor Reference",
			ReferencePageIDs: []string{"p1", "p2"},
			StyleNotes:       "soft ink lines, warm interior lighting",
		},
		Budget: adapt.Budget{MaxCalls: 10, MaxTotalTokens: 20000},
	}

	gen := &mockGen{
		handler: func(call int, req adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			if call == 1 && !strings.Contains(req.Prompt, "soft ink lines") {
				t.Errorf("prompt missing StyleRef notes")
			}
			if call == 2 && !strings.Contains(req.Prompt, "温青") {
				t.Errorf("chunk 2 prompt missing previously discovered character 温青")
			}
			return &adapt.GenerateResponse{
				Text: `{
					"scenes": [{
						"setting": "Apartment doorway",
						"mood": "tense",
						"panels": [{
							"shot": "medium close-up",
							"action": "Wen Qing answers from behind the door",
							"characters": ["温青"],
							"dialogues": [{"speaker": "温青", "text": "在，我有备用钥匙", "kind": "speech"}],
							"prompt": "anime panel, young woman near apartment door, soft ink lines, warm interior lighting"
						}]
					}],
					"newCharacters": [{
						"id": "char_wenqing",
						"name": "温青",
						"appearance": "long dark hair, expressive eyes",
						"outfit": "casual indoor sweater",
						"traits": ["guarded"]
					}]
				}`,
				Usage: adapt.Usage{Calls: 1, InputTokens: 500, OutputTokens: 180, TotalTokens: 680},
			}, nil
		},
	}

	res, err := adapt.AdaptChapter(
		context.Background(),
		proj,
		novel.Chapter{ID: "746748421", Title: "第272章"},
		novel.ChapterContent{HTML: string(rawHTML)},
		gen,
		adapt.AdaptOptions{MaxChunkRunes: 120},
	)
	if err != nil {
		t.Fatalf("AdaptChapter: %v", err)
	}
	if res.Chunks < 2 {
		t.Fatalf("expected real chapter to split into >= 2 chunks, got %d", res.Chunks)
	}
	if len(res.Storyboard.Scenes) != res.Chunks {
		t.Fatalf("expected %d scenes, got %d", res.Chunks, len(res.Storyboard.Scenes))
	}
	if len(proj.Characters.Characters) != 1 || proj.Characters.Characters[0].Name != "温青" {
		t.Fatalf("expected deduplicated character sheet with 温青, got %+v", proj.Characters.Characters)
	}

	// Project JSON round-trip check.
	blob, err := json.Marshal(proj)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	var roundTrip adapt.Project
	if err := json.Unmarshal(blob, &roundTrip); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}
	if roundTrip.Storyboards["746748421"] == nil || roundTrip.Usage.Calls != int64(res.Chunks) {
		t.Fatalf("round-trip project mismatch: %+v", roundTrip)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

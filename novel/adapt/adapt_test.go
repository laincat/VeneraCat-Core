package adapt_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestAdaptChapterCheckpointResumeBudgetAndTransientFailure(t *testing.T) {
	html := "<p>第一段：夜雨打在窗玻璃上，少年推开房门。</p>" +
		"<p>第二段：走廊尽头的灯光微弱，一名女子静静站着。</p>" +
		"<p>第三段：两人对视片刻，交换了手中的铜钥匙。</p>"
	chapter := novel.Chapter{ID: "ch-resume", Title: "Chapter Resume"}
	content := novel.ChapterContent{HTML: html}

	proj := &adapt.Project{
		ID:     "proj-resume",
		Title:  "Resume Novel",
		Budget: adapt.Budget{MaxCalls: 1},
	}

	var seenChunks []string
	transientFailed := false
	gen := &mockGen{
		handler: func(call int, req adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			switch {
			case strings.Contains(req.Prompt, "Chunk: 1/3"):
				seenChunks = append(seenChunks, "1/3")
				return &adapt.GenerateResponse{
					Text: `{
						"scenes":[{"setting":"Rainy room","panels":[{"shot":"wide","action":"Boy opens door","prompt":"rainy room doorway"}]}],
						"newCharacters":[{"id":"c_boy","name":"Lin","appearance":"short black hair","outfit":"dark coat"}]
					}`,
					Usage: adapt.Usage{Calls: 1, InputTokens: 40, OutputTokens: 20, TotalTokens: 60},
				}, nil
			case strings.Contains(req.Prompt, "Chunk: 2/3"):
				seenChunks = append(seenChunks, "2/3")
				if !strings.Contains(req.Prompt, "Lin (c_boy)") {
					t.Errorf("chunk 2 prompt missing accumulated character Lin: %s", req.Prompt)
				}
				if !transientFailed {
					transientFailed = true
					return nil, errors.New("upstream 503 temporary outage")
				}
				return &adapt.GenerateResponse{
					Text: `{
						"scenes":[{"setting":"Dim hallway","panels":[{"shot":"medium","action":"Woman waits","prompt":"dim hallway portrait"}]}],
						"newCharacters":[{"id":"c_woman","name":"Wen","appearance":"long hair","outfit":"beige coat"}]
					}`,
					Usage: adapt.Usage{Calls: 1, InputTokens: 50, OutputTokens: 30, TotalTokens: 80},
				}, nil
			case strings.Contains(req.Prompt, "Chunk: 3/3"):
				seenChunks = append(seenChunks, "3/3")
				if !strings.Contains(req.Prompt, "Lin (c_boy)") || !strings.Contains(req.Prompt, "Wen (c_woman)") {
					t.Errorf("chunk 3 prompt missing accumulated characters: %s", req.Prompt)
				}
				return &adapt.GenerateResponse{
					Text:  `{"scenes":[{"setting":"Hallway end","panels":[{"shot":"close-up","action":"Key exchange","prompt":"brass key close-up"}]}]}`,
					Usage: adapt.Usage{Calls: 1, InputTokens: 45, OutputTokens: 25, TotalTokens: 70},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected prompt: %s", req.Prompt)
			}
		},
	}

	// Pass 1: MaxCalls=1 stops before chunk 2 and returns a serialisable checkpoint.
	res1, err := adapt.AdaptChapter(context.Background(), proj, chapter, content, gen, adapt.AdaptOptions{MaxChunkRunes: 25})
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("pass 1 error = %v, want ErrBudgetExceeded", err)
	}
	if res1 == nil || res1.Checkpoint == nil {
		t.Fatalf("pass 1 expected non-nil partial result with checkpoint, got %+v", res1)
	}
	if res1.Chunks != 3 || res1.CompletedChunks != 1 || res1.Checkpoint.CompletedChunks != 1 {
		t.Fatalf("pass 1 progress = chunks:%d completed:%d cp:%+v", res1.Chunks, res1.CompletedChunks, res1.Checkpoint)
	}
	if len(res1.Checkpoint.Storyboard.Scenes) != 1 || len(res1.Checkpoint.Characters.Characters) != 1 {
		t.Fatalf("pass 1 checkpoint content mismatch: %+v", res1.Checkpoint)
	}
	if res1.Checkpoint.Usage.Calls != 1 || res1.Checkpoint.Usage.TotalTokens != 60 {
		t.Fatalf("pass 1 checkpoint usage = %+v", res1.Checkpoint.Usage)
	}
	if proj.Storyboards[chapter.ID] != nil {
		t.Fatal("incomplete storyboard must not be committed to proj.Storyboards yet")
	}

	// Round-trip checkpoint through JSON to prove serialisability.
	cpRaw, err := json.Marshal(res1.Checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	var cp1 adapt.AdaptCheckpoint
	if err := json.Unmarshal(cpRaw, &cp1); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}

	// Pass 2: Raise budget, resume from cp1; chunk 2 hits a transient error.
	// Chunk 1 must NOT be re-called.
	proj.Budget.MaxCalls = 5
	res2, err := adapt.AdaptChapter(context.Background(), proj, chapter, content, gen, adapt.AdaptOptions{
		Checkpoint: &cp1,
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("pass 2 error = %v, want transient 503", err)
	}
	if res2 == nil || res2.Checkpoint == nil || res2.Checkpoint.CompletedChunks != 1 {
		t.Fatalf("pass 2 checkpoint should remain at 1 completed chunk: %+v", res2)
	}

	// Pass 3: Resume from pass 2 checkpoint; chunks 2 and 3 finish cleanly.
	cp2Raw, err := json.Marshal(res2.Checkpoint)
	if err != nil {
		t.Fatalf("marshal pass 2 checkpoint: %v", err)
	}
	var cp2 adapt.AdaptCheckpoint
	if err := json.Unmarshal(cp2Raw, &cp2); err != nil {
		t.Fatalf("unmarshal pass 2 checkpoint: %v", err)
	}
	res3, err := adapt.AdaptChapter(context.Background(), proj, chapter, content, gen, adapt.AdaptOptions{
		Checkpoint: &cp2,
	})
	if err != nil {
		t.Fatalf("pass 3 failed: %v", err)
	}
	if res3.Checkpoint != nil {
		t.Fatalf("completed run should clear checkpoint, got %+v", res3.Checkpoint)
	}
	if res3.CompletedChunks != 3 || len(res3.Storyboard.Scenes) != 3 {
		t.Fatalf("pass 3 scenes = %d completed = %d", len(res3.Storyboard.Scenes), res3.CompletedChunks)
	}
	if got := strings.Join(seenChunks, ","); got != "1/3,2/3,2/3,3/3" {
		t.Fatalf("unexpected chunk call sequence: got %q, want 1/3,2/3,2/3,3/3", got)
	}
	// Scene and panel auto-IDs must continue seamlessly across checkpoint boundaries.
	if res3.Storyboard.Scenes[0].ID != "s1" || res3.Storyboard.Scenes[1].ID != "s2" || res3.Storyboard.Scenes[2].ID != "s3" {
		t.Fatalf("scene IDs not continuous: %+v", res3.Storyboard.Scenes)
	}
	if res3.Storyboard.Scenes[0].Panels[0].ID != "p1" || res3.Storyboard.Scenes[1].Panels[0].ID != "p2" || res3.Storyboard.Scenes[2].Panels[0].ID != "p3" {
		t.Fatalf("panel IDs not continuous: %+v", res3.Storyboard.Scenes)
	}
	// Total usage must count chunk 1 once, chunk 2 once, chunk 3 once (3 calls, 210 tokens).
	if proj.Usage.Calls != 3 || proj.Usage.TotalTokens != 210 {
		t.Fatalf("project usage = %+v, want calls=3 totalTokens=210", proj.Usage)
	}
	if res3.Usage.Calls != 3 || res3.Usage.TotalTokens != 210 {
		t.Fatalf("result usage = %+v, want calls=3 totalTokens=210", res3.Usage)
	}
	if len(proj.Characters.Characters) != 2 {
		t.Fatalf("expected 2 merged characters, got %+v", proj.Characters.Characters)
	}
	if proj.Storyboards[chapter.ID] == nil || len(proj.Storyboards[chapter.ID].Scenes) != 3 {
		t.Fatalf("final storyboard not stored on project: %+v", proj.Storyboards[chapter.ID])
	}
}

func TestAdaptChapterCheckpointResumeIntoFreshProjectRespectsBudget(t *testing.T) {
	html := "<p>第一段内容长度刚好。</p><p>第二段内容长度刚好。</p>"
	chapter := novel.Chapter{ID: "ch-fresh", Title: "Fresh"}
	content := novel.ChapterContent{HTML: html}

	gen := &mockGen{
		handler: func(call int, req adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			return &adapt.GenerateResponse{
				Text: `{
					"scenes":[{"setting":"Hall","panels":[{"shot":"wide","action":"Walk","prompt":"hall"}]}],
					"newCharacters":[{"id":"c1","name":"Alice","appearance":"blue eyes"}]
				}`,
				Usage: adapt.Usage{Calls: 1, InputTokens: 50, OutputTokens: 50, TotalTokens: 100},
			}, nil
		},
	}

	proj1 := &adapt.Project{ID: "p1", Title: "Novel", Budget: adapt.Budget{MaxCalls: 1}}
	res1, err := adapt.AdaptChapter(context.Background(), proj1, chapter, content, gen, adapt.AdaptOptions{MaxChunkRunes: 12})
	if !errors.Is(err, adapt.ErrBudgetExceeded) || res1 == nil || res1.Checkpoint == nil {
		t.Fatalf("expected budget stop with checkpoint, got res=%+v err=%v", res1, err)
	}

	// Resuming into a fresh Project with zero Usage and MaxCalls=1 must still
	// hydrate prior spend from the checkpoint and stop immediately.
	freshTight := &adapt.Project{ID: "p1", Title: "Novel", Budget: adapt.Budget{MaxCalls: 1}}
	resBlocked, err := adapt.AdaptChapter(context.Background(), freshTight, chapter, content, gen, adapt.AdaptOptions{
		Checkpoint: res1.Checkpoint,
	})
	if !errors.Is(err, adapt.ErrBudgetExceeded) {
		t.Fatalf("fresh project with MaxCalls=1 should hit ErrBudgetExceeded, got %v", err)
	}
	if resBlocked == nil || resBlocked.Checkpoint == nil || resBlocked.Checkpoint.CompletedChunks != 1 {
		t.Fatalf("expected checkpoint preserved, got %+v", resBlocked)
	}
	if gen.calls != 1 {
		t.Fatalf("generator should not be called when hydrated checkpoint already hit budget, calls=%d", gen.calls)
	}

	// With MaxCalls=2, resuming into a fresh Project restores Alice and finishes chunk 2.
	freshOK := &adapt.Project{ID: "p1", Title: "Novel", Budget: adapt.Budget{MaxCalls: 2}}
	res2, err := adapt.AdaptChapter(context.Background(), freshOK, chapter, content, gen, adapt.AdaptOptions{
		Checkpoint: res1.Checkpoint,
	})
	if err != nil {
		t.Fatalf("fresh resume failed: %v", err)
	}
	if gen.calls != 2 || len(freshOK.Characters.Characters) != 1 || freshOK.Usage.Calls != 2 {
		t.Fatalf("fresh resume state mismatch: calls=%d chars=%+v usage=%+v", gen.calls, freshOK.Characters.Characters, freshOK.Usage)
	}
	if len(res2.Storyboard.Scenes) != 2 {
		t.Fatalf("expected 2 scenes, got %d", len(res2.Storyboard.Scenes))
	}
}

func TestAdaptChapterCheckpointValidationAndAlreadyComplete(t *testing.T) {
	html := "<p>第一段内容。</p><p>第二段内容。</p>"
	chapter := novel.Chapter{ID: "ch1", Title: "Ch1"}
	content := novel.ChapterContent{HTML: html}
	gen := &mockGen{
		handler: func(call int, _ adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			return nil, errors.New("should not be called")
		},
	}

	badCases := []struct {
		name string
		opts adapt.AdaptOptions
	}{
		{
			name: "chapter-mismatch",
			opts: adapt.AdaptOptions{MaxChunkRunes: 8, Checkpoint: &adapt.AdaptCheckpoint{ChapterID: "other", TotalChunks: 2, CompletedChunks: 1}},
		},
		{
			name: "max-runes-mismatch",
			opts: adapt.AdaptOptions{MaxChunkRunes: 10, Checkpoint: &adapt.AdaptCheckpoint{ChapterID: "ch1", MaxChunkRunes: 8, TotalChunks: 2, CompletedChunks: 1}},
		},
		{
			name: "total-chunks-mismatch",
			opts: adapt.AdaptOptions{MaxChunkRunes: 8, Checkpoint: &adapt.AdaptCheckpoint{ChapterID: "ch1", TotalChunks: 5, CompletedChunks: 1}},
		},
		{
			name: "negative-completed",
			opts: adapt.AdaptOptions{MaxChunkRunes: 8, Checkpoint: &adapt.AdaptCheckpoint{ChapterID: "ch1", TotalChunks: 2, CompletedChunks: -1}},
		},
		{
			name: "completed-exceeds-total",
			opts: adapt.AdaptOptions{MaxChunkRunes: 8, Checkpoint: &adapt.AdaptCheckpoint{ChapterID: "ch1", TotalChunks: 2, CompletedChunks: 3}},
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			proj := &adapt.Project{ID: "p", Title: "T"}
			if _, err := adapt.AdaptChapter(context.Background(), proj, chapter, content, gen, tc.opts); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}

	// Already-complete checkpoint commits storyboard with 0 generator calls.
	proj := &adapt.Project{ID: "p", Title: "T"}
	doneCP := &adapt.AdaptCheckpoint{
		ChapterID:       "ch1",
		TotalChunks:     2,
		CompletedChunks: 2,
		MaxChunkRunes:   8,
		Storyboard: &adapt.Storyboard{
			ChapterID: "ch1",
			Scenes:    []adapt.Scene{{ID: "s1", Setting: "Room", Panels: []adapt.Panel{{ID: "p1", Shot: "wide", Action: "stand", Prompt: "room"}}}},
		},
		Usage: adapt.Usage{Calls: 2, TotalTokens: 120},
	}
	res, err := adapt.AdaptChapter(context.Background(), proj, chapter, content, gen, adapt.AdaptOptions{Checkpoint: doneCP})
	if err != nil {
		t.Fatalf("already-complete checkpoint failed: %v", err)
	}
	if gen.calls != 0 || res.Checkpoint != nil || proj.Storyboards["ch1"] == nil {
		t.Fatalf("already-complete checkpoint unexpected state: calls=%d cp=%v sb=%v", gen.calls, res.Checkpoint, proj.Storyboards["ch1"])
	}
}

func TestBudgetBoundaryLimits(t *testing.T) {
	cases := []struct {
		name   string
		budget adapt.Budget
		below  adapt.Usage
		atLim  adapt.Usage
		image  bool
	}{
		{
			name:   "max-calls",
			budget: adapt.Budget{MaxCalls: 2},
			below:  adapt.Usage{Calls: 1},
			atLim:  adapt.Usage{Calls: 2},
		},
		{
			name:   "max-input-tokens",
			budget: adapt.Budget{MaxInputTokens: 100},
			below:  adapt.Usage{InputTokens: 99},
			atLim:  adapt.Usage{InputTokens: 100},
		},
		{
			name:   "max-output-tokens",
			budget: adapt.Budget{MaxOutputTokens: 50},
			below:  adapt.Usage{OutputTokens: 49},
			atLim:  adapt.Usage{OutputTokens: 50},
		},
		{
			name:   "max-total-tokens",
			budget: adapt.Budget{MaxTotalTokens: 150},
			below:  adapt.Usage{TotalTokens: 149},
			atLim:  adapt.Usage{TotalTokens: 150},
		},
		{
			name:   "max-image-calls",
			budget: adapt.Budget{MaxImageCalls: 3},
			below:  adapt.Usage{ImageCalls: 2},
			atLim:  adapt.Usage{ImageCalls: 3},
			image:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := func(u adapt.Usage) error {
				if tc.image {
					return u.CheckNextImageCall(tc.budget)
				}
				return u.CheckNextCall(tc.budget)
			}
			if err := check(tc.below); err != nil {
				t.Fatalf("below limit %+v should pass, got %v", tc.below, err)
			}
			if err := check(tc.atLim); !errors.Is(err, adapt.ErrBudgetExceeded) {
				t.Fatalf("at limit %+v should return ErrBudgetExceeded, got %v", tc.atLim, err)
			}
		})
	}
}

func TestAdaptChapterCancellationAndConcurrencySafety(t *testing.T) {
	html := "<p>第一段内容触发切分。</p><p>第二段内容触发切分。</p><p>第三段内容触发切分。</p>"
	chapter := novel.Chapter{ID: "ch-cancel", Title: "Cancel"}
	content := novel.ChapterContent{HTML: html}

	// Cancel right after chunk 1 completes -> chunk 1 is saved in checkpoint.
	ctx, cancel := context.WithCancel(context.Background())
	proj := &adapt.Project{ID: "p-cancel", Title: "Cancel Novel"}
	gen := &mockGen{
		handler: func(call int, _ adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
			if call == 1 {
				cancel()
			}
			return &adapt.GenerateResponse{
				Text:  `{"scenes":[{"setting":"Room","panels":[{"shot":"wide","action":"sit","prompt":"room"}]}]}`,
				Usage: adapt.Usage{Calls: 1, InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
			}, nil
		},
	}

	partial, err := adapt.AdaptChapter(ctx, proj, chapter, content, gen, adapt.AdaptOptions{MaxChunkRunes: 12})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if partial == nil || partial.Checkpoint == nil || partial.Checkpoint.CompletedChunks != 1 {
		t.Fatalf("expected checkpoint at chunk 1 after cancel, got %+v", partial)
	}

	// Concurrently resume several independent projects from the SAME checkpoint
	// under -race to verify AdaptChapter never mutates opts.Checkpoint.
	sharedCP := partial.Checkpoint
	const workers = 6
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			p := &adapt.Project{
				ID:    fmt.Sprintf("worker-%d", workerID),
				Title: "Concurrent Novel",
			}
			wGen := &mockGen{
				handler: func(_ int, req adapt.GenerateRequest) (*adapt.GenerateResponse, error) {
					if strings.Contains(req.Prompt, "Chunk: 1/3") {
						return nil, errors.New("chunk 1 should have been skipped")
					}
					return &adapt.GenerateResponse{
						Text:  `{"scenes":[{"setting":"Corridor","panels":[{"shot":"medium","action":"move","prompt":"corridor"}]}]}`,
						Usage: adapt.Usage{Calls: 1, InputTokens: 15, OutputTokens: 15, TotalTokens: 30},
					}, nil
				},
			}
			res, err := adapt.AdaptChapter(context.Background(), p, chapter, content, wGen, adapt.AdaptOptions{
				Checkpoint: sharedCP,
			})
			if err != nil {
				errCh <- err
				return
			}
			if res.CompletedChunks != 3 || len(res.Storyboard.Scenes) != 3 || wGen.calls != 2 {
				errCh <- fmt.Errorf("worker %d unexpected result: completed=%d scenes=%d calls=%d", workerID, res.CompletedChunks, len(res.Storyboard.Scenes), wGen.calls)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if sharedCP.CompletedChunks != 1 || len(sharedCP.Storyboard.Scenes) != 1 {
		t.Fatalf("shared checkpoint was mutated during concurrent resume: %+v", sharedCP)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

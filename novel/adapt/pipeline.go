package adapt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/novel"
)

// GenerateRequest is one text-model prompt issued by the adaptation pipeline.
type GenerateRequest struct {
	System string
	Prompt string
}

// GenerateResponse is the text output and token usage from one generator call.
type GenerateResponse struct {
	Text  string
	Usage Usage
}

// TextGenerator abstracts the text model backend (aiapi, Magpie plugin, or
// external AI agent).
type TextGenerator interface {
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}

// AIAPIGenerator adapts aiapi (chat, responses, messages) + host.Transport to
// TextGenerator. It is safe for concurrent use once configured.
type AIAPIGenerator struct {
	Config    aiapi.Config
	Model     string
	MaxTokens int
	// Temperature is optional; nil omits the parameter (recommended for
	// reasoning models such as DeepSeek Reasoner and Codex).
	Temperature *float64
	// JSONMode requests JSON object output when supported by the target
	// protocol (chat: response_format.type="json_object"; responses:
	// text.format.type="json_object").
	JSONMode bool
	// ReasoningEffort sets reasoning.effort on the Responses API when non-empty
	// (for example "low", "medium", "high").
	ReasoningEffort string
	Transport       host.Transport
}

// BuildRequest builds a protocol-native host.Request via aiapi.BuildRequest.
func (g *AIAPIGenerator) BuildRequest(req GenerateRequest) (*host.Request, error) {
	if strings.TrimSpace(g.Model) == "" {
		return nil, errors.New("adapt: aiapi model is required")
	}
	var payload any
	switch g.Config.Protocol {
	case aiapi.Chat:
		msgs := make([]map[string]string, 0, 2)
		if req.System != "" {
			msgs = append(msgs, map[string]string{"role": "system", "content": req.System})
		}
		msgs = append(msgs, map[string]string{"role": "user", "content": req.Prompt})
		body := map[string]any{
			"model":    g.Model,
			"messages": msgs,
		}
		if g.MaxTokens > 0 {
			body["max_tokens"] = g.MaxTokens
		}
		if g.Temperature != nil {
			body["temperature"] = *g.Temperature
		}
		if g.JSONMode {
			body["response_format"] = map[string]string{"type": "json_object"}
		}
		payload = body
	case aiapi.Responses:
		body := map[string]any{
			"model": g.Model,
			"input": req.Prompt,
		}
		if req.System != "" {
			body["instructions"] = req.System
		}
		if g.MaxTokens > 0 {
			body["max_output_tokens"] = g.MaxTokens
		}
		if g.Temperature != nil {
			body["temperature"] = *g.Temperature
		}
		if strings.TrimSpace(g.ReasoningEffort) != "" {
			body["reasoning"] = map[string]string{"effort": strings.TrimSpace(g.ReasoningEffort)}
		}
		if g.JSONMode {
			body["text"] = map[string]any{
				"format": map[string]string{"type": "json_object"},
			}
		}
		payload = body
	case aiapi.Messages:
		maxTokens := g.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 4096
		}
		body := map[string]any{
			"model":      g.Model,
			"max_tokens": maxTokens,
			"messages": []map[string]string{
				{"role": "user", "content": req.Prompt},
			},
		}
		if req.System != "" {
			body["system"] = req.System
		}
		if g.Temperature != nil {
			body["temperature"] = *g.Temperature
		}
		payload = body
	default:
		return nil, fmt.Errorf("adapt: unsupported aiapi protocol %q", g.Config.Protocol)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("adapt: marshal request: %w", err)
	}
	return aiapi.BuildRequest(g.Config, raw)
}

// ParseAIAPIResponse extracts text and token usage from an aiapi protocol response.
func ParseAIAPIResponse(protocol aiapi.Protocol, status int, body []byte) (*GenerateResponse, error) {
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("adapt: aiapi HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	switch protocol {
	case aiapi.Chat:
		var out struct {
			Choices []struct {
				Message struct {
					Content          json.RawMessage `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("adapt: decode chat response: %w", err)
		}
		if len(out.Choices) == 0 {
			return nil, errors.New("adapt: chat response has empty choices")
		}
		text, err := extractChatMessageContent(out.Choices[0].Message.Content)
		if err != nil {
			return nil, fmt.Errorf("adapt: decode chat message content: %w", err)
		}
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("adapt: chat response has empty choices")
		}
		u := Usage{Calls: 1, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens, TotalTokens: out.Usage.TotalTokens}
		if u.TotalTokens == 0 {
			u.TotalTokens = u.InputTokens + u.OutputTokens
		}
		return &GenerateResponse{Text: text, Usage: u}, nil

	case aiapi.Responses:
		var out struct {
			Status string `json:"status"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			OutputText string `json:"output_text"`
			Output     []struct {
				Type    string `json:"type"`
				Content []struct {
					Type    string `json:"type"`
					Text    string `json:"text"`
					Refusal string `json:"refusal"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				TotalTokens  int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("adapt: decode responses payload: %w", err)
		}
		if out.Error != nil && strings.TrimSpace(out.Error.Message) != "" {
			return nil, fmt.Errorf("adapt: responses error (%s): %s", strings.TrimSpace(out.Error.Code), strings.TrimSpace(out.Error.Message))
		}
		text := strings.TrimSpace(out.OutputText)
		if text == "" {
			var parts []string
			var refusals []string
			for _, item := range out.Output {
				if item.Type != "" && item.Type != "message" {
					continue
				}
				for _, c := range item.Content {
					switch c.Type {
					case "", "output_text", "text":
						if strings.TrimSpace(c.Text) != "" {
							parts = append(parts, c.Text)
						}
					case "refusal":
						if strings.TrimSpace(c.Refusal) != "" {
							refusals = append(refusals, strings.TrimSpace(c.Refusal))
						}
					}
				}
			}
			if len(parts) == 0 && len(refusals) > 0 {
				return nil, fmt.Errorf("adapt: responses refused output: %s", strings.Join(refusals, "; "))
			}
			text = strings.TrimSpace(strings.Join(parts, "\n"))
		}
		if text == "" {
			return nil, errors.New("adapt: responses payload has no text output")
		}
		u := Usage{Calls: 1, InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, TotalTokens: out.Usage.TotalTokens}
		if u.TotalTokens == 0 {
			u.TotalTokens = u.InputTokens + u.OutputTokens
		}
		return &GenerateResponse{Text: text, Usage: u}, nil

	case aiapi.Messages:
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("adapt: decode messages response: %w", err)
		}
		var parts []string
		for _, c := range out.Content {
			if c.Type == "" || c.Type == "text" {
				if strings.TrimSpace(c.Text) != "" {
					parts = append(parts, c.Text)
				}
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		if text == "" {
			return nil, errors.New("adapt: messages response has no text blocks")
		}
		u := Usage{
			Calls:        1,
			InputTokens:  out.Usage.InputTokens,
			OutputTokens: out.Usage.OutputTokens,
			TotalTokens:  out.Usage.InputTokens + out.Usage.OutputTokens,
		}
		return &GenerateResponse{Text: text, Usage: u}, nil

	default:
		return nil, fmt.Errorf("adapt: unsupported aiapi protocol %q", protocol)
	}
}

func extractChatMessageContent(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if trimmed[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return "", err
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "" || b.Type == "text" || b.Type == "output_text" {
				if strings.TrimSpace(b.Text) != "" {
					parts = append(parts, b.Text)
				}
			}
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", errors.New("unexpected content JSON type")
}

// Generate implements TextGenerator via aiapi and host.Transport.
func (g *AIAPIGenerator) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if g.Transport == nil {
		return nil, errors.New("adapt: nil transport")
	}
	httpReq, err := g.BuildRequest(req)
	if err != nil {
		return nil, redactError(err, g.Config.APIKey)
	}
	resp, err := g.Transport.Do(ctx, httpReq)
	if err != nil {
		return nil, redactError(fmt.Errorf("adapt: transport: %w", err), g.Config.APIKey)
	}
	if resp == nil {
		return nil, errors.New("adapt: nil transport response")
	}
	out, err := ParseAIAPIResponse(g.Config.Protocol, resp.Status, resp.Body)
	if err != nil {
		return nil, redactError(err, g.Config.APIKey)
	}
	return out, nil
}

// ChunkOutput is the strict JSON schema expected from the text generator for
// one chapter chunk.
type ChunkOutput struct {
	Scenes        []Scene     `json:"scenes"`
	NewCharacters []Character `json:"newCharacters,omitempty"`
}

// ParseAdaptationJSON parses and strictly validates a model's JSON response.
// Invalid JSON or missing required storyboard fields always return an error
// wrapping ErrInvalidStoryboard.
func ParseAdaptationJSON(raw string) (*ChunkOutput, error) {
	cleaned := stripJSONFence(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("%w: empty output", ErrInvalidStoryboard)
	}
	dec := json.NewDecoder(strings.NewReader(cleaned))
	dec.UseNumber()
	var out ChunkOutput
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStoryboard, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing content after JSON object", ErrInvalidStoryboard)
	}
	if len(out.Scenes) == 0 {
		return nil, fmt.Errorf("%w: scenes must be a non-empty array", ErrInvalidStoryboard)
	}
	for si := range out.Scenes {
		sc := &out.Scenes[si]
		if strings.TrimSpace(sc.Setting) == "" {
			return nil, fmt.Errorf("%w: scenes[%d].setting is required", ErrInvalidStoryboard, si)
		}
		if len(sc.Panels) == 0 {
			return nil, fmt.Errorf("%w: scenes[%d].panels must be non-empty", ErrInvalidStoryboard, si)
		}
		for pi := range sc.Panels {
			p := &sc.Panels[pi]
			if strings.TrimSpace(p.Shot) == "" {
				return nil, fmt.Errorf("%w: scenes[%d].panels[%d].shot is required", ErrInvalidStoryboard, si, pi)
			}
			if strings.TrimSpace(p.Action) == "" {
				return nil, fmt.Errorf("%w: scenes[%d].panels[%d].action is required", ErrInvalidStoryboard, si, pi)
			}
			if strings.TrimSpace(p.Prompt) == "" {
				return nil, fmt.Errorf("%w: scenes[%d].panels[%d].prompt is required", ErrInvalidStoryboard, si, pi)
			}
			switch p.ImageStatus {
			case "":
				p.ImageStatus = ImagePending
			case ImagePending, ImageGenerating, ImageDone, ImageFailed:
			default:
				return nil, fmt.Errorf("%w: scenes[%d].panels[%d].imageStatus %q is invalid", ErrInvalidStoryboard, si, pi, p.ImageStatus)
			}
		}
	}
	for ci := range out.NewCharacters {
		if strings.TrimSpace(out.NewCharacters[ci].Name) == "" {
			return nil, fmt.Errorf("%w: newCharacters[%d].name is required", ErrInvalidStoryboard, ci)
		}
	}
	return &out, nil
}

func stripJSONFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "<think>") {
		if end := strings.Index(trimmed, "</think>"); end >= 0 {
			trimmed = strings.TrimSpace(trimmed[end+len("</think>"):])
		}
	}
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	firstNL := strings.IndexByte(trimmed, '\n')
	if firstNL < 0 {
		return trimmed
	}
	rest := trimmed[firstNL+1:]
	if idx := strings.LastIndex(rest, "```"); idx >= 0 {
		return strings.TrimSpace(rest[:idx])
	}
	return trimmed
}

// MergeCharacters incrementally merges incoming characters into sheet.
// Existing non-empty Appearance and Outfit descriptions are preserved so
// visual identity stays consistent across chapters. Returns newly added
// characters.
func MergeCharacters(sheet *CharacterSheet, incoming []Character, chapterID string) []Character {
	if sheet == nil || len(incoming) == 0 {
		return nil
	}
	var added []Character
	for _, inc := range incoming {
		name := strings.TrimSpace(inc.Name)
		if name == "" {
			continue
		}
		idx := findCharacterIndex(sheet.Characters, inc)
		if idx < 0 {
			c := inc
			c.Name = name
			if strings.TrimSpace(c.ID) == "" {
				c.ID = fmt.Sprintf("char_%d", len(sheet.Characters)+1)
			}
			if c.FirstChapterID == "" {
				c.FirstChapterID = chapterID
			}
			c.Aliases = dedupeStrings(c.Aliases)
			c.Traits = dedupeStrings(c.Traits)
			sheet.Characters = append(sheet.Characters, c)
			added = append(added, c)
			continue
		}
		existing := &sheet.Characters[idx]
		// Preserve established visual appearance and outfit; only fill if empty.
		if strings.TrimSpace(existing.Appearance) == "" && strings.TrimSpace(inc.Appearance) != "" {
			existing.Appearance = strings.TrimSpace(inc.Appearance)
		}
		if strings.TrimSpace(existing.Outfit) == "" && strings.TrimSpace(inc.Outfit) != "" {
			existing.Outfit = strings.TrimSpace(inc.Outfit)
		}
		if !strings.EqualFold(existing.Name, name) {
			existing.Aliases = dedupeStrings(append(existing.Aliases, name))
		}
		existing.Aliases = dedupeStrings(append(existing.Aliases, inc.Aliases...))
		existing.Traits = dedupeStrings(append(existing.Traits, inc.Traits...))
		if strings.TrimSpace(inc.Notes) != "" {
			if strings.TrimSpace(existing.Notes) == "" {
				existing.Notes = strings.TrimSpace(inc.Notes)
			} else if !strings.Contains(existing.Notes, strings.TrimSpace(inc.Notes)) {
				existing.Notes += "; " + strings.TrimSpace(inc.Notes)
			}
		}
	}
	return added
}

func findCharacterIndex(list []Character, target Character) int {
	tID := strings.ToLower(strings.TrimSpace(target.ID))
	tName := strings.ToLower(strings.TrimSpace(target.Name))
	for i, c := range list {
		if tID != "" && strings.ToLower(strings.TrimSpace(c.ID)) == tID {
			return i
		}
		if tName != "" && strings.ToLower(strings.TrimSpace(c.Name)) == tName {
			return i
		}
		for _, a := range c.Aliases {
			if tName != "" && strings.ToLower(strings.TrimSpace(a)) == tName {
				return i
			}
		}
	}
	return -1
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// AdaptOptions tunes chapter chunking and resumable execution.
type AdaptOptions struct {
	MaxChunkRunes int              `json:"maxChunkRunes,omitempty"`
	Checkpoint    *AdaptCheckpoint `json:"checkpoint,omitempty"`
}

// AdaptCheckpoint is the serialisable resume state for a multi-chunk chapter
// adaptation. Passing it back via AdaptOptions.Checkpoint skips already
// completed chunks and continues from CompletedChunks without re-billing
// earlier calls.
type AdaptCheckpoint struct {
	ChapterID       string         `json:"chapterId"`
	ChapterTitle    string         `json:"chapterTitle,omitempty"`
	TotalChunks     int            `json:"totalChunks"`
	CompletedChunks int            `json:"completedChunks"`
	MaxChunkRunes   int            `json:"maxChunkRunes,omitempty"`
	Storyboard      *Storyboard    `json:"storyboard,omitempty"`
	Characters      CharacterSheet `json:"characters"`
	AddedCharacters []Character    `json:"addedCharacters,omitempty"`
	Usage           Usage          `json:"usage"`
	SceneSeq        int            `json:"sceneSeq,omitempty"`
	PanelSeq        int            `json:"panelSeq,omitempty"`
}

// AdaptResult reports the generated storyboard, newly added characters,
// usage spent on this chapter, and any resume checkpoint when adaptation
// stopped before completing all chunks.
type AdaptResult struct {
	Storyboard      *Storyboard      `json:"storyboard,omitempty"`
	AddedCharacters []Character      `json:"addedCharacters,omitempty"`
	Chunks          int              `json:"chunks"`
	CompletedChunks int              `json:"completedChunks"`
	Usage           Usage            `json:"usage"`
	Checkpoint      *AdaptCheckpoint `json:"checkpoint,omitempty"`
}

const systemPrompt = `You are a comic storyboard adapter. Convert the provided novel excerpt into strict JSON matching {"scenes":[{"id":"s1","setting":"...","mood":"...","panels":[{"id":"p1","shot":"medium shot","action":"...","characters":["..."],"dialogues":[{"speaker":"...","text":"...","kind":"speech"}],"prompt":"visual prompt","imageStatus":"pending"}]}],"newCharacters":[{"id":"c1","name":"...","appearance":"...","outfit":"...","traits":["..."]}]}. Output JSON only, with no prose outside the JSON object.`

// BuildChapterPrompt builds the prompt for one chapter chunk, embedding
// optional local style reference notes and the existing character sheet.
func BuildChapterPrompt(proj *Project, chapter novel.Chapter, chunk string, chunkIndex, totalChunks int) GenerateRequest {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Novel: %s\nChapter: %s (%s)\nChunk: %d/%d\n", proj.Title, chapter.Title, chapter.ID, chunkIndex+1, totalChunks)
	if proj.StyleRef != nil {
		b.WriteString("Style reference (local personal use only): ")
		if proj.StyleRef.ComicTitle != "" {
			b.WriteString(proj.StyleRef.ComicTitle)
			if proj.StyleRef.StyleNotes != "" {
				b.WriteString(" — ")
			}
		}
		if proj.StyleRef.StyleNotes != "" {
			b.WriteString(proj.StyleRef.StyleNotes)
		}
		b.WriteByte('\n')
	}
	if len(proj.Characters.Characters) > 0 {
		b.WriteString("Existing characters (keep appearance consistent):\n")
		for _, c := range proj.Characters.Characters {
			fmt.Fprintf(&b, "- %s (%s): appearance=%s; outfit=%s\n", c.Name, c.ID, c.Appearance, c.Outfit)
		}
	}
	b.WriteString("\nExcerpt:\n")
	b.WriteString(chunk)
	return GenerateRequest{System: systemPrompt, Prompt: b.String()}
}

// AdaptChapter adapts a single chapter into a Storyboard, merging newly
// discovered characters into proj.Characters and enforcing proj.Budget before
// every generator call.
//
// When a multi-chunk chapter stops early due to ErrBudgetExceeded, context
// cancellation, a transient generator error, or invalid chunk JSON,
// AdaptChapter returns a non-nil AdaptResult carrying a serialisable
// Checkpoint alongside the error. Passing that Checkpoint back via
// AdaptOptions.Checkpoint skips completed chunks and resumes without
// re-billing earlier calls.
func AdaptChapter(ctx context.Context, proj *Project, chapter novel.Chapter, content novel.ChapterContent, gen TextGenerator, opts AdaptOptions) (*AdaptResult, error) {
	if proj == nil {
		return nil, errors.New("adapt: nil project")
	}
	if gen == nil {
		return nil, errors.New("adapt: nil text generator")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cp := opts.Checkpoint
	maxChunkRunes := opts.MaxChunkRunes
	if maxChunkRunes <= 0 && cp != nil && cp.MaxChunkRunes > 0 {
		maxChunkRunes = cp.MaxChunkRunes
	}
	if maxChunkRunes <= 0 {
		maxChunkRunes = DefaultMaxChunkRunes
	}
	if opts.MaxChunkRunes > 0 && cp != nil && cp.MaxChunkRunes > 0 && opts.MaxChunkRunes != cp.MaxChunkRunes {
		return nil, fmt.Errorf("adapt: checkpoint maxChunkRunes %d does not match requested %d", cp.MaxChunkRunes, opts.MaxChunkRunes)
	}

	plain := ExtractText(content.HTML)
	chunks := ChunkText(plain, maxChunkRunes)
	if len(chunks) == 0 {
		return nil, ErrEmptyChapter
	}

	sb := &Storyboard{
		ChapterID:    chapter.ID,
		ChapterTitle: chapter.Title,
		Scenes:       []Scene{},
	}
	res := &AdaptResult{
		Storyboard: sb,
		Chunks:     len(chunks),
	}

	startChunk := 0
	sceneSeq := 0
	panelSeq := 0
	if cp != nil {
		if cp.ChapterID != "" && cp.ChapterID != chapter.ID {
			return nil, fmt.Errorf("adapt: checkpoint chapter %q does not match chapter %q", cp.ChapterID, chapter.ID)
		}
		if cp.TotalChunks > 0 && cp.TotalChunks != len(chunks) {
			return nil, fmt.Errorf("adapt: checkpoint total chunks %d does not match chapter chunks %d", cp.TotalChunks, len(chunks))
		}
		if cp.CompletedChunks < 0 || cp.CompletedChunks > len(chunks) {
			return nil, fmt.Errorf("adapt: checkpoint completed chunks %d out of range [0,%d]", cp.CompletedChunks, len(chunks))
		}
		startChunk = cp.CompletedChunks
		res.CompletedChunks = cp.CompletedChunks
		if cp.Storyboard != nil {
			sb = cloneStoryboard(cp.Storyboard)
			sb.ChapterID = chapter.ID
			if sb.ChapterTitle == "" {
				sb.ChapterTitle = chapter.Title
			}
			if sb.Scenes == nil {
				sb.Scenes = []Scene{}
			}
			res.Storyboard = sb
		}
		if len(cp.Characters.Characters) > 0 {
			MergeCharacters(&proj.Characters, cloneCharacters(cp.Characters.Characters), chapter.ID)
		}
		res.AddedCharacters = cloneCharacters(cp.AddedCharacters)
		res.Usage = cp.Usage
		if proj.Usage == (Usage{}) && cp.Usage != (Usage{}) {
			proj.Usage = cp.Usage
		}
		sceneSeq = cp.SceneSeq
		if sceneSeq < len(sb.Scenes) {
			sceneSeq = len(sb.Scenes)
		}
		panelSeq = cp.PanelSeq
		existingPanels := countStoryboardPanels(sb)
		if panelSeq < existingPanels {
			panelSeq = existingPanels
		}
	}

	partialResult := func(completed int) *AdaptResult {
		res.CompletedChunks = completed
		res.Checkpoint = &AdaptCheckpoint{
			ChapterID:       chapter.ID,
			ChapterTitle:    chapter.Title,
			TotalChunks:     len(chunks),
			CompletedChunks: completed,
			MaxChunkRunes:   maxChunkRunes,
			Storyboard:      cloneStoryboard(sb),
			Characters:      cloneCharacterSheet(proj.Characters),
			AddedCharacters: cloneCharacters(res.AddedCharacters),
			Usage:           res.Usage,
			SceneSeq:        sceneSeq,
			PanelSeq:        panelSeq,
		}
		return res
	}

	for i := startChunk; i < len(chunks); i++ {
		if err := ctx.Err(); err != nil {
			return partialResult(i), err
		}
		if err := proj.Usage.CheckNextCall(proj.Budget); err != nil {
			return partialResult(i), err
		}
		req := BuildChapterPrompt(proj, chapter, chunks[i], i, len(chunks))
		genResp, err := gen.Generate(ctx, req)
		if err != nil {
			return partialResult(i), fmt.Errorf("adapt: chunk %d/%d generate: %w", i+1, len(chunks), err)
		}
		if genResp == nil {
			return partialResult(i), fmt.Errorf("adapt: chunk %d/%d generate: empty generator response", i+1, len(chunks))
		}
		delta := genResp.Usage
		if delta.Calls == 0 {
			delta.Calls = 1
		}
		proj.Usage.Add(delta)
		res.Usage.Add(delta)

		parsed, err := ParseAdaptationJSON(genResp.Text)
		if err != nil {
			return partialResult(i), fmt.Errorf("adapt: chunk %d/%d: %w", i+1, len(chunks), err)
		}

		added := MergeCharacters(&proj.Characters, parsed.NewCharacters, chapter.ID)
		res.AddedCharacters = append(res.AddedCharacters, added...)

		for _, sc := range parsed.Scenes {
			sceneSeq++
			if strings.TrimSpace(sc.ID) == "" {
				sc.ID = fmt.Sprintf("s%d", sceneSeq)
			}
			for pi := range sc.Panels {
				panelSeq++
				if strings.TrimSpace(sc.Panels[pi].ID) == "" {
					sc.Panels[pi].ID = fmt.Sprintf("p%d", panelSeq)
				}
			}
			sb.Scenes = append(sb.Scenes, sc)
		}
		res.CompletedChunks = i + 1
	}

	res.CompletedChunks = len(chunks)
	res.Checkpoint = nil
	if proj.Storyboards == nil {
		proj.Storyboards = map[string]*Storyboard{}
	}
	proj.Storyboards[chapter.ID] = sb
	return res, nil
}

func cloneStoryboard(sb *Storyboard) *Storyboard {
	if sb == nil {
		return nil
	}
	out := &Storyboard{
		ChapterID:    sb.ChapterID,
		ChapterTitle: sb.ChapterTitle,
		Scenes:       make([]Scene, len(sb.Scenes)),
	}
	for si, sc := range sb.Scenes {
		clonedScene := Scene{
			ID:      sc.ID,
			Setting: sc.Setting,
			Mood:    sc.Mood,
			Panels:  make([]Panel, len(sc.Panels)),
		}
		for pi, p := range sc.Panels {
			clonedPanel := p
			if len(p.Characters) > 0 {
				clonedPanel.Characters = append([]string(nil), p.Characters...)
			}
			if len(p.Dialogues) > 0 {
				clonedPanel.Dialogues = append([]Dialogue(nil), p.Dialogues...)
			}
			clonedScene.Panels[pi] = clonedPanel
		}
		out.Scenes[si] = clonedScene
	}
	return out
}

func cloneCharacter(c Character) Character {
	out := c
	if len(c.Aliases) > 0 {
		out.Aliases = append([]string(nil), c.Aliases...)
	}
	if len(c.Traits) > 0 {
		out.Traits = append([]string(nil), c.Traits...)
	}
	return out
}

func cloneCharacters(in []Character) []Character {
	if len(in) == 0 {
		return nil
	}
	out := make([]Character, len(in))
	for i, c := range in {
		out[i] = cloneCharacter(c)
	}
	return out
}

func cloneCharacterSheet(sheet CharacterSheet) CharacterSheet {
	return CharacterSheet{
		Characters: cloneCharacters(sheet.Characters),
	}
}

func countStoryboardPanels(sb *Storyboard) int {
	if sb == nil {
		return 0
	}
	n := 0
	for _, sc := range sb.Scenes {
		n += len(sc.Panels)
	}
	return n
}

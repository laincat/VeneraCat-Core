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
// TextGenerator.
type AIAPIGenerator struct {
	Config    aiapi.Config
	Model     string
	MaxTokens int
	Transport host.Transport
}

// BuildRequest builds a protocol-native host.Request via aiapi.BuildRequest.
func (g *AIAPIGenerator) BuildRequest(req GenerateRequest) (*host.Request, error) {
	if strings.TrimSpace(g.Model) == "" {
		return nil, errors.New("adapt: aiapi model is required")
	}
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	var payload any
	switch g.Config.Protocol {
	case aiapi.Chat:
		msgs := make([]map[string]string, 0, 2)
		if req.System != "" {
			msgs = append(msgs, map[string]string{"role": "system", "content": req.System})
		}
		msgs = append(msgs, map[string]string{"role": "user", "content": req.Prompt})
		payload = map[string]any{
			"model":    g.Model,
			"messages": msgs,
		}
	case aiapi.Responses:
		body := map[string]any{
			"model": g.Model,
			"input": req.Prompt,
		}
		if req.System != "" {
			body["instructions"] = req.System
		}
		payload = body
	case aiapi.Messages:
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

// ParseResponse extracts text and token usage from an aiapi protocol response.
func ParseAIAPIResponse(protocol aiapi.Protocol, status int, body []byte) (*GenerateResponse, error) {
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("adapt: aiapi HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	switch protocol {
	case aiapi.Chat:
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
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
		if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
			return nil, errors.New("adapt: chat response has empty choices")
		}
		u := Usage{Calls: 1, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens, TotalTokens: out.Usage.TotalTokens}
		if u.TotalTokens == 0 {
			u.TotalTokens = u.InputTokens + u.OutputTokens
		}
		return &GenerateResponse{Text: out.Choices[0].Message.Content, Usage: u}, nil

	case aiapi.Responses:
		var out struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
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
		text := strings.TrimSpace(out.OutputText)
		if text == "" {
			var parts []string
			for _, item := range out.Output {
				for _, c := range item.Content {
					if strings.TrimSpace(c.Text) != "" {
						parts = append(parts, c.Text)
					}
				}
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

// Generate implements TextGenerator via aiapi and host.Transport.
func (g *AIAPIGenerator) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if g.Transport == nil {
		return nil, errors.New("adapt: nil transport")
	}
	httpReq, err := g.BuildRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := g.Transport.Do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("adapt: transport: %w", err)
	}
	return ParseAIAPIResponse(g.Config.Protocol, resp.Status, resp.Body)
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

// AdaptOptions tunes chapter chunking.
type AdaptOptions struct {
	MaxChunkRunes int
}

// AdaptResult reports the generated storyboard, newly added characters, and
// usage spent on this chapter.
type AdaptResult struct {
	Storyboard      *Storyboard
	AddedCharacters []Character
	Chunks          int
	Usage           Usage
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
func AdaptChapter(ctx context.Context, proj *Project, chapter novel.Chapter, content novel.ChapterContent, gen TextGenerator, opts AdaptOptions) (*AdaptResult, error) {
	if proj == nil {
		return nil, errors.New("adapt: nil project")
	}
	if gen == nil {
		return nil, errors.New("adapt: nil text generator")
	}
	plain := ExtractText(content.HTML)
	chunks := ChunkText(plain, opts.MaxChunkRunes)
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

	sceneSeq := 0
	panelSeq := 0
	for i, chunk := range chunks {
		if err := proj.Usage.CheckNextCall(proj.Budget); err != nil {
			return nil, err
		}
		req := BuildChapterPrompt(proj, chapter, chunk, i, len(chunks))
		genResp, err := gen.Generate(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("adapt: chunk %d/%d generate: %w", i+1, len(chunks), err)
		}
		delta := genResp.Usage
		if delta.Calls == 0 {
			delta.Calls = 1
		}
		proj.Usage.Add(delta)
		res.Usage.Add(delta)

		parsed, err := ParseAdaptationJSON(genResp.Text)
		if err != nil {
			return nil, fmt.Errorf("adapt: chunk %d/%d: %w", i+1, len(chunks), err)
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
	}

	if proj.Storyboards == nil {
		proj.Storyboards = map[string]*Storyboard{}
	}
	proj.Storyboards[chapter.ID] = sb
	return res, nil
}

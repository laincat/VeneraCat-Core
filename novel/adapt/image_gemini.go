package adapt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/laincat/veneracat-core/host"
)

// DefaultGeminiBaseURL is the default Gemini REST API root.
const DefaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiImageGenerator renders static comic panels via Gemini's native
// generateContent endpoint over host.Transport. It supports optional inline
// reference images (style sheets and character turnarounds) and distinguishes
// safety policy blocks (ErrContentBlocked) from transient errors.
//
// It is safe for concurrent use once configured.
type GeminiImageGenerator struct {
	BaseURL string
	APIKey  string
	Model   string
	// ResponseModalities defaults to ["IMAGE"] when empty. Callers may set
	// ["TEXT", "IMAGE"] for models that require text alongside image output.
	ResponseModalities []string
	Partition          string
	Transport          host.Transport
}

// BuildRequest builds a Gemini generateContent host.Request for req.
// Credentials are placed only in the x-goog-api-key header, never in the URL.
func (g *GeminiImageGenerator) BuildRequest(req PanelImageRequest) (*host.Request, error) {
	base, err := validateAPIBaseURL(g.BaseURL, DefaultGeminiBaseURL, "gemini")
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(g.APIKey)
	if key == "" {
		return nil, errors.New("adapt: gemini API key is required")
	}
	if err := host.ValidHeader("x-goog-api-key", key); err != nil {
		return nil, errors.New("adapt: gemini API key is not a valid header value")
	}
	model := strings.TrimPrefix(strings.TrimSpace(g.Model), "models/")
	if model == "" || strings.ContainsAny(model, "/?# \t\r\n") {
		return nil, errors.New("adapt: gemini model is required and must be a single path segment")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("adapt: panel image prompt is required")
	}

	parts := make([]map[string]any, 0, 1+len(req.References)*2)
	for _, ref := range req.References {
		if len(ref.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(ref.MIMEType)
		if mimeType == "" {
			mimeType = detectImageMIME(ref.Data)
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return nil, fmt.Errorf("adapt: gemini reference image has unsupported MIME type %q", mimeType)
		}
		if note := formatReferenceHint(ref); note != "" {
			parts = append(parts, map[string]any{"text": note})
		}
		parts = append(parts, map[string]any{
			"inlineData": map[string]string{
				"mimeType": mimeType,
				"data":     base64.StdEncoding.EncodeToString(ref.Data),
			},
		})
	}
	parts = append(parts, map[string]any{"text": prompt})

	modalities := g.ResponseModalities
	if len(modalities) == 0 {
		modalities = []string{"IMAGE"}
	} else {
		modalities = append([]string(nil), modalities...)
	}

	genCfg := map[string]any{
		"responseModalities": modalities,
	}
	if ar := strings.TrimSpace(req.AspectRatio); ar != "" {
		genCfg["imageConfig"] = map[string]string{
			"aspectRatio": ar,
		}
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": genCfg,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("adapt: marshal gemini request: %w", err)
	}

	header := make(http.Header, 3)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	header.Set("x-goog-api-key", key)

	return &host.Request{
		Method:    http.MethodPost,
		URL:       base + "/models/" + model + ":generateContent",
		Header:    header,
		Body:      raw,
		Cookies:   false,
		Partition: g.Partition,
	}, nil
}

// GenerateImage implements ImageGenerator via Gemini generateContent and
// host.Transport. Errors never disclose g.APIKey.
func (g *GeminiImageGenerator) GenerateImage(ctx context.Context, req PanelImageRequest) (*PanelImageResult, error) {
	if g.Transport == nil {
		return nil, errors.New("adapt: nil transport")
	}
	httpReq, err := g.BuildRequest(req)
	if err != nil {
		return nil, redactError(err, g.APIKey)
	}
	resp, err := g.Transport.Do(ctx, httpReq)
	if err != nil {
		return nil, redactError(fmt.Errorf("adapt: gemini transport: %w", err), g.APIKey)
	}
	if resp == nil {
		return nil, errors.New("adapt: nil transport response")
	}
	out, err := ParseGeminiImageResponse(resp.Status, resp.Body)
	if err != nil {
		return nil, redactError(err, g.APIKey)
	}
	return out, nil
}

// ParseGeminiImageResponse parses a Gemini generateContent HTTP response into
// a PanelImageResult. Policy blocks such as IMAGE_SAFETY or promptFeedback
// blockReason return an error wrapping ErrContentBlocked.
func ParseGeminiImageResponse(status int, body []byte) (*PanelImageResult, error) {
	if status < 200 || status >= 300 {
		var apiErr struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != nil && strings.TrimSpace(apiErr.Error.Message) != "" {
			msg := strings.TrimSpace(apiErr.Error.Message)
			if isGeminiBlockedReason(apiErr.Error.Status) || isGeminiBlockedMessage(msg) {
				return nil, fmt.Errorf("%w: gemini HTTP %d: %s", ErrContentBlocked, status, msg)
			}
			return nil, fmt.Errorf("adapt: gemini HTTP %d (%s): %s", status, strings.TrimSpace(apiErr.Error.Status), msg)
		}
		return nil, fmt.Errorf("adapt: gemini HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}

	type inlineBlob struct {
		MIMETypeSnake string `json:"mime_type"`
		MIMETypeCamel string `json:"mimeType"`
		Data          string `json:"data"`
	}
	var out struct {
		PromptFeedback *struct {
			BlockReason        string `json:"blockReason"`
			BlockReasonMessage string `json:"blockReasonMessage"`
		} `json:"promptFeedback"`
		Candidates []struct {
			FinishReason  string `json:"finishReason"`
			FinishMessage string `json:"finishMessage"`
			Content       struct {
				Parts []struct {
					Text            string      `json:"text"`
					InlineDataCamel *inlineBlob `json:"inlineData"`
					InlineDataSnake *inlineBlob `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			TotalTokenCount      int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("adapt: decode gemini response: %w", err)
	}

	if out.PromptFeedback != nil && strings.TrimSpace(out.PromptFeedback.BlockReason) != "" {
		reason := strings.TrimSpace(out.PromptFeedback.BlockReason)
		if msg := strings.TrimSpace(out.PromptFeedback.BlockReasonMessage); msg != "" {
			return nil, fmt.Errorf("%w: gemini prompt blocked (%s): %s", ErrContentBlocked, reason, msg)
		}
		return nil, fmt.Errorf("%w: gemini prompt blocked (%s)", ErrContentBlocked, reason)
	}

	var blockedReason string
	var blockedMsg string
	var revisedParts []string

	for _, cand := range out.Candidates {
		if isGeminiBlockedReason(cand.FinishReason) {
			if blockedReason == "" {
				blockedReason = strings.TrimSpace(cand.FinishReason)
				blockedMsg = strings.TrimSpace(cand.FinishMessage)
			}
		}
		for _, part := range cand.Content.Parts {
			if t := strings.TrimSpace(part.Text); t != "" {
				revisedParts = append(revisedParts, t)
			}
			blob := part.InlineDataCamel
			if blob == nil {
				blob = part.InlineDataSnake
			}
			if blob == nil || strings.TrimSpace(blob.Data) == "" {
				continue
			}
			decoded, err := decodeBase64Flexible(blob.Data)
			if err != nil {
				return nil, fmt.Errorf("adapt: decode gemini inlineData: %w", err)
			}
			if len(decoded) == 0 {
				continue
			}
			mimeType := strings.TrimSpace(blob.MIMETypeCamel)
			if mimeType == "" {
				mimeType = strings.TrimSpace(blob.MIMETypeSnake)
			}
			if mimeType == "" {
				mimeType = detectImageMIME(decoded)
			}
			u := Usage{
				ImageCalls:   1,
				InputTokens:  out.UsageMetadata.PromptTokenCount,
				OutputTokens: out.UsageMetadata.CandidatesTokenCount,
				TotalTokens:  out.UsageMetadata.TotalTokenCount,
			}
			if u.TotalTokens == 0 && (u.InputTokens > 0 || u.OutputTokens > 0) {
				u.TotalTokens = u.InputTokens + u.OutputTokens
			}
			return &PanelImageResult{
				MIMEType:      mimeType,
				Data:          decoded,
				RevisedPrompt: strings.Join(revisedParts, "\n"),
				Usage:         u,
			}, nil
		}
	}

	if blockedReason != "" {
		if blockedMsg != "" {
			return nil, fmt.Errorf("%w: gemini finishReason %s: %s", ErrContentBlocked, blockedReason, blockedMsg)
		}
		return nil, fmt.Errorf("%w: gemini finishReason %s", ErrContentBlocked, blockedReason)
	}
	return nil, errors.New("adapt: gemini response contained no image inlineData")
}

func formatReferenceHint(ref ImageRef) string {
	label := strings.TrimSpace(ref.Label)
	switch ref.Kind {
	case ImageRefStyle:
		if label != "" {
			return "Personal local style reference (" + label + "): match linework, palette, and rendering style."
		}
		return "Personal local style reference: match linework, palette, and rendering style."
	case ImageRefCharacter:
		if label != "" {
			return "Character visual reference (" + label + "): preserve facial features, hairstyle, and outfit."
		}
		return "Character visual reference: preserve facial features, hairstyle, and outfit."
	default:
		if label != "" {
			return "Visual reference (" + label + ")."
		}
		return ""
	}
}

func isGeminiBlockedReason(reason string) bool {
	upper := strings.ToUpper(strings.TrimSpace(reason))
	switch upper {
	case "IMAGE_SAFETY",
		"IMAGE_PROHIBITED_CONTENT",
		"IMAGE_RECITATION",
		"SAFETY",
		"PROHIBITED_CONTENT",
		"RECITATION",
		"BLOCKLIST",
		"SPII":
		return true
	default:
		return strings.Contains(upper, "SAFETY") || strings.Contains(upper, "PROHIBITED")
	}
}

func isGeminiBlockedMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "image_safety") ||
		strings.Contains(lower, "prohibited_content") ||
		strings.Contains(lower, "safety policy") ||
		strings.Contains(lower, "blocked by safety")
}

func decodeBase64Flexible(raw string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, raw)
	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(cleaned)
}

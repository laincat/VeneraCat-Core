package adapt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/laincat/veneracat-core/host"
)

// DefaultOpenAIImageBaseURL is the default OpenAI Images API root.
const DefaultOpenAIImageBaseURL = "https://api.openai.com/v1"

// OpenAIImageGenerator renders static comic panels via the standard OpenAI
// Images API over host.Transport with Bearer authentication:
//   - POST /images/generations when no inline reference image bytes are attached
//   - POST /images/edits (multipart/form-data) when inline reference images are attached
//
// It is safe for concurrent use once configured.
type OpenAIImageGenerator struct {
	BaseURL string
	APIKey  string
	Model   string
	Size    string
	Quality string
	// OutputFormat optionally specifies "png", "jpeg", or "webp" for models
	// supporting output_format (such as gpt-image-1).
	OutputFormat string
	// ResponseFormat optionally overrides response_format. When empty, models
	// other than gpt-image* default to "b64_json".
	ResponseFormat string
	Partition      string
	Transport      host.Transport
}

// BuildRequest builds a standard OpenAI Images host.Request for req.
func (g *OpenAIImageGenerator) BuildRequest(req PanelImageRequest) (*host.Request, error) {
	base, err := validateAPIBaseURL(g.BaseURL, DefaultOpenAIImageBaseURL, "openai images")
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(g.APIKey)
	if key == "" {
		return nil, errors.New("adapt: openai images API key is required")
	}
	if err := host.ValidHeader("Authorization", "Bearer "+key); err != nil {
		return nil, errors.New("adapt: openai images API key is not a valid header value")
	}
	model := strings.TrimSpace(g.Model)
	if model == "" {
		return nil, errors.New("adapt: openai images model is required")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("adapt: panel image prompt is required")
	}

	size := strings.TrimSpace(req.Size)
	if size == "" {
		size = strings.TrimSpace(g.Size)
	}
	quality := strings.TrimSpace(g.Quality)
	outputFmt := strings.TrimSpace(g.OutputFormat)
	respFmt := strings.TrimSpace(g.ResponseFormat)
	if respFmt == "" && !isGPTImageModel(model) {
		respFmt = "b64_json"
	}

	inlineRefs := collectInlineRefs(req.References)
	if len(inlineRefs) > 0 {
		return g.buildEditsRequest(base, key, model, prompt, size, quality, outputFmt, respFmt, inlineRefs)
	}

	body := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	if size != "" {
		body["size"] = size
	}
	if quality != "" {
		body["quality"] = quality
	}
	if outputFmt != "" {
		body["output_format"] = outputFmt
	}
	if respFmt != "" {
		body["response_format"] = respFmt
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("adapt: marshal openai images request: %w", err)
	}

	header := make(http.Header, 3)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	header.Set("Authorization", "Bearer "+key)

	return &host.Request{
		Method:    http.MethodPost,
		URL:       base + "/images/generations",
		Header:    header,
		Body:      raw,
		Cookies:   false,
		Partition: g.Partition,
	}, nil
}

func (g *OpenAIImageGenerator) buildEditsRequest(base, key, model, prompt, size, quality, outputFmt, respFmt string, refs []ImageRef) (*host.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fields := []struct {
		name  string
		value string
	}{
		{"model", model},
		{"prompt", prompt},
		{"n", "1"},
		{"size", size},
		{"quality", quality},
		{"output_format", outputFmt},
		{"response_format", respFmt},
	}
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if err := mw.WriteField(f.name, f.value); err != nil {
			return nil, fmt.Errorf("adapt: write multipart field %s: %w", f.name, err)
		}
	}

	fieldName := "image"
	if len(refs) > 1 {
		fieldName = "image[]"
	}
	for i, ref := range refs {
		mimeType := strings.TrimSpace(ref.MIMEType)
		if mimeType == "" {
			mimeType = detectImageMIME(ref.Data)
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return nil, fmt.Errorf("adapt: openai reference image has unsupported MIME type %q", mimeType)
		}
		fileName := fmt.Sprintf("ref_%d%s", i, extForMIME(mimeType))
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
		partHeader.Set("Content-Type", mimeType)
		pw, err := mw.CreatePart(partHeader)
		if err != nil {
			return nil, fmt.Errorf("adapt: create multipart image part: %w", err)
		}
		if _, err := pw.Write(ref.Data); err != nil {
			return nil, fmt.Errorf("adapt: write multipart image bytes: %w", err)
		}
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("adapt: finalize multipart body: %w", err)
	}

	header := make(http.Header, 3)
	header.Set("Content-Type", mw.FormDataContentType())
	header.Set("Accept", "application/json")
	header.Set("Authorization", "Bearer "+key)

	return &host.Request{
		Method:    http.MethodPost,
		URL:       base + "/images/edits",
		Header:    header,
		Body:      buf.Bytes(),
		Cookies:   false,
		Partition: g.Partition,
	}, nil
}

// GenerateImage implements ImageGenerator via OpenAI Images and host.Transport.
// Errors never disclose g.APIKey.
func (g *OpenAIImageGenerator) GenerateImage(ctx context.Context, req PanelImageRequest) (*PanelImageResult, error) {
	if g.Transport == nil {
		return nil, errors.New("adapt: nil transport")
	}
	httpReq, err := g.BuildRequest(req)
	if err != nil {
		return nil, redactError(err, g.APIKey)
	}
	resp, err := g.Transport.Do(ctx, httpReq)
	if err != nil {
		return nil, redactError(fmt.Errorf("adapt: openai images transport: %w", err), g.APIKey)
	}
	if resp == nil {
		return nil, errors.New("adapt: nil transport response")
	}
	out, err := ParseOpenAIImageResponse(resp.Status, resp.Body)
	if err != nil {
		return nil, redactError(err, g.APIKey)
	}
	return out, nil
}

// ParseOpenAIImageResponse decodes an OpenAI Images API response containing
// b64_json image bytes. Content policy rejections wrap ErrContentBlocked.
func ParseOpenAIImageResponse(status int, body []byte) (*PanelImageResult, error) {
	if status < 200 || status >= 300 {
		var apiErr struct {
			Error *struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != nil && strings.TrimSpace(apiErr.Error.Message) != "" {
			code := strings.TrimSpace(apiErr.Error.Code)
			errType := strings.TrimSpace(apiErr.Error.Type)
			msg := strings.TrimSpace(apiErr.Error.Message)
			if isOpenAIContentBlocked(code, errType, msg) {
				return nil, fmt.Errorf("%w: openai HTTP %d (%s): %s", ErrContentBlocked, status, code, msg)
			}
			return nil, fmt.Errorf("adapt: openai images HTTP %d (%s): %s", status, code, msg)
		}
		return nil, fmt.Errorf("adapt: openai images HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}

	var out struct {
		OutputFormat string `json:"output_format"`
		Data         []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("adapt: decode openai images response: %w", err)
	}
	if len(out.Data) == 0 || strings.TrimSpace(out.Data[0].B64JSON) == "" {
		return nil, errors.New("adapt: openai images response has no b64_json data")
	}

	decoded, err := decodeBase64Flexible(out.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("adapt: decode openai b64_json: %w", err)
	}
	if len(decoded) == 0 {
		return nil, errors.New("adapt: openai images response decoded to empty bytes")
	}

	mimeType := mimeFromOpenAIFormat(out.OutputFormat)
	if mimeType == "" {
		mimeType = detectImageMIME(decoded)
	}

	u := Usage{
		ImageCalls:   1,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
		TotalTokens:  out.Usage.TotalTokens,
	}
	if u.TotalTokens == 0 && (u.InputTokens > 0 || u.OutputTokens > 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}

	return &PanelImageResult{
		MIMEType:      mimeType,
		Data:          decoded,
		RevisedPrompt: strings.TrimSpace(out.Data[0].RevisedPrompt),
		Usage:         u,
	}, nil
}

func collectInlineRefs(refs []ImageRef) []ImageRef {
	var out []ImageRef
	for _, r := range refs {
		if len(r.Data) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func isGPTImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image")
}

func mimeFromOpenAIFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return "image/png"
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func isOpenAIContentBlocked(code, errType, msg string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "content_policy_violation", "moderation_blocked", "safety_violation":
		return true
	}
	lowerMsg := strings.ToLower(msg)
	if strings.EqualFold(errType, "image_generation_user_error") &&
		(strings.Contains(lowerMsg, "safety system") || strings.Contains(lowerMsg, "content policy")) {
		return true
	}
	return false
}

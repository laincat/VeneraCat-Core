package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
)

type aiConfig struct {
	Protocol         aiapi.Protocol `json:"protocol"`
	BaseURL          string         `json:"baseUrl"`
	APIKey           string         `json:"apiKey"`
	AnthropicVersion string         `json:"anthropicVersion"`
	Partition        string         `json:"partition"`
	RequireMasking   bool           `json:"requireMasking"`
}

func (raw aiConfig) standardConfig() aiapi.Config {
	return aiapi.Config{Protocol: raw.Protocol, BaseURL: raw.BaseURL, APIKey: raw.APIKey,
		AnthropicVersion: raw.AnthropicVersion, Partition: raw.Partition}
}

type aiRequest struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Headers    http.Header `json:"headers"`
	BodyBase64 string      `json:"bodyBase64,omitempty"`
	Cookies    bool        `json:"cookies"`
	Partition  string      `json:"partition,omitempty"`
}

type aiResponse struct {
	Status       int                  `json:"status"`
	Headers      http.Header          `json:"headers"`
	BodyBase64   string               `json:"bodyBase64"`
	FinalURL     string               `json:"finalUrl"`
	FinishReason string               `json:"finishReason,omitempty"`
	FinishDetail string               `json:"finishDetail,omitempty"`
	Outcome      aiapi.FinishReason   `json:"outcome,omitempty"`
	Usage        *aiapi.TokenUsage    `json:"usage"`
	Error        *aiapi.ProviderError `json:"error,omitempty"`
	UsageEvent   *aiapi.UsageEvent    `json:"usageEvent,omitempty"`
}

type aiStreamResponse struct {
	Status       int                `json:"status"`
	Headers      http.Header        `json:"headers"`
	Text         string             `json:"text"`
	Reasoning    string             `json:"reasoning,omitempty"`
	FinishReason string             `json:"finishReason,omitempty"`
	FinishDetail string             `json:"finishDetail,omitempty"`
	Outcome      aiapi.FinishReason `json:"outcome,omitempty"`
	Usage        *aiapi.TokenUsage  `json:"usage"`
	FinalURL     string             `json:"finalUrl"`
	Streamed     bool               `json:"streamed"`
	UsageEvent   *aiapi.UsageEvent  `json:"usageEvent,omitempty"`
}

func configFromJSON(configJSON string) (aiConfig, error) {
	var raw aiConfig
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return aiConfig{}, errors.New("mobile: invalid AI config JSON")
	}
	return raw, nil
}

func guardAI(err *error) {
	if recover() != nil {
		*err = errors.New("mobile: internal AI error")
	}
}

func toWire(r *host.Request) aiRequest {
	return aiRequest{Method: r.Method, URL: r.URL, Headers: r.Header, Cookies: r.Cookies, Partition: r.Partition,
		BodyBase64: base64.StdEncoding.EncodeToString(r.Body)}
}

// BuildAIRequest returns a standard request for a client-owned transport.
// The returned headers carry the caller's credential and must be kept private.
func (c *Core) BuildAIRequest(configJSON, bodyJSON string) (_ string, err error) {
	defer guard(&err)
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		return "", err
	}
	if cfg.RequireMasking {
		return "", &aiapi.ProviderError{Code: aiapi.ErrCodeMaskingFailed, Message: "required masking needs RunAIRequest"}
	}
	request, err := aiapi.BuildRequest(cfg.standardConfig(), json.RawMessage(bodyJSON))
	if err != nil {
		return "", err
	}
	return encode(toWire(request))
}

// BuildAIModelsRequest returns a model-list GET request.
func (c *Core) BuildAIModelsRequest(configJSON string) (_ string, err error) {
	defer guard(&err)
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		return "", err
	}
	request, err := aiapi.BuildModelsRequest(cfg.standardConfig())
	if err != nil {
		return "", err
	}
	return encode(toWire(request))
}

// AIContentMasker is an optional aiapi-ext/1 callback object for one request.
// The client keeps the restoration state in this object. RestoreDelta is called
// separately for text and reasoning, with final=true at protocol completion.
type AIContentMasker interface {
	APIVersion() string
	MaskRequest(protocol, bodyJSON string) (string, error)
	RestoreBody(bodyJSON string) (string, error)
	RestoreDelta(kind, text string, final bool) (string, error)
}

type aiMasker struct{ client AIContentMasker }

func (m aiMasker) APIVersion() string { return m.client.APIVersion() }

func (m aiMasker) MaskRequest(_ context.Context, protocol aiapi.Protocol, body json.RawMessage) (json.RawMessage, aiapi.MaskSession, error) {
	masked, err := m.client.MaskRequest(string(protocol), string(body))
	return json.RawMessage(masked), m, err
}

func (m aiMasker) RestoreBody(body json.RawMessage) (json.RawMessage, error) {
	restored, err := m.client.RestoreBody(string(body))
	return json.RawMessage(restored), err
}

func (m aiMasker) RestoreDelta(kind aiapi.StreamEventKind, text string, final bool) (string, error) {
	return m.client.RestoreDelta(string(kind), text, final)
}

type mobileAIStreamTransport struct{ bridge *streamBridge }

func (t mobileAIStreamTransport) DoStream(ctx context.Context, req *host.Request, onHead func(*host.Response) error, onChunk func([]byte) error) (*host.Response, error) {
	asResponse := func(head *streamAnswer) *host.Response {
		return &host.Response{Status: head.Status, Header: head.Header, FinalURL: head.URL}
	}
	answer, err := t.bridge.sendWithHead(ctx, req.Partition, req.Method, req.URL, req.Header, req.Body,
		func(head *streamAnswer) error { return onHead(asResponse(head)) }, onChunk)
	if answer == nil {
		return nil, err
	}
	return asResponse(answer), err
}

// SendAIRequest performs a bounded whole-response call. For HTTP failures, the
// legacy status response contains a structured error and an empty bodyBase64.
func (c *Core) SendAIRequest(callID int64, configJSON, bodyJSON string) (_ string, err error) {
	defer guardAI(&err)
	defer c.endCall(callID)
	return c.runAIRequest(callID, configJSON, bodyJSON, false, nil, true)
}

// SendAIRequestStream collects a streamed standard AI answer through HostStream.
func (c *Core) SendAIRequestStream(callID int64, configJSON, bodyJSON string) (_ string, err error) {
	defer guardAI(&err)
	defer c.endCall(callID)
	return c.runAIRequest(callID, configJSON, bodyJSON, true, nil, true)
}

// RunAIRequest executes a standard request using the optional per-call masker.
// Request and provider failures are returned in JSON with an error object.
func (c *Core) RunAIRequest(callID int64, configJSON, bodyJSON string, streamed bool, masker AIContentMasker) (_ string, err error) {
	defer guardAI(&err)
	defer c.endCall(callID)
	return c.runAIRequest(callID, configJSON, bodyJSON, streamed, masker, false)
}

func (c *Core) runAIRequest(callID int64, configJSON, bodyJSON string, streamed bool, masker AIContentMasker, legacy bool) (string, error) {
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		if legacy {
			return "", err
		}
		return encode(map[string]any{"error": &aiapi.ProviderError{Code: aiapi.ErrCodeInvalidRequest, Message: "invalid AI config JSON"}})
	}
	pipeline := aiapi.Pipeline{Transport: c.bridge, AllowRawResponse: legacy}
	if c.stream != nil {
		pipeline.StreamTransport = mobileAIStreamTransport{bridge: &streamBridge{h: c.stream, assign: nextHostRequestID}}
	}
	if masker != nil {
		pipeline.Extensions.Masker = aiMasker{client: masker}
	}
	ctx, done := c.ctx(callID)
	defer done()
	var result *aiapi.CallResult
	meta := aiapi.RequestMeta{RequireMasking: cfg.RequireMasking}
	if streamed {
		result, err = pipeline.DoStream(ctx, cfg.standardConfig(), json.RawMessage(bodyJSON), nil, meta)
	} else {
		result, err = pipeline.Do(ctx, cfg.standardConfig(), json.RawMessage(bodyJSON), meta)
	}
	var failure *aiapi.ProviderError
	if err != nil {
		if legacy && (streamed || result == nil || result.Status == 0) {
			return "", err
		}
		if !errors.As(err, &failure) {
			failure = &aiapi.ProviderError{Code: aiapi.ErrCodeInvalidRequest, Message: "invalid AI request"}
		}
		if result == nil {
			result = &aiapi.CallResult{Streamed: streamed}
		}
	}
	if streamed {
		return encode(struct {
			aiStreamResponse
			Error *aiapi.ProviderError `json:"error,omitempty"`
		}{aiStreamResponse: aiStreamResponse{
			Status: result.Status, Headers: result.Header, Text: result.Text, Reasoning: result.Reasoning,
			FinishReason: result.FinishReason, FinishDetail: result.FinishDetail, Outcome: result.Outcome, Usage: result.Usage,
			FinalURL: result.FinalURL, Streamed: true, UsageEvent: result.UsageEvent,
		}, Error: failure})
	}
	return encode(struct {
		aiResponse
		Streamed bool `json:"streamed"`
	}{aiResponse: aiResponse{
		Status: result.Status, Headers: result.Header, BodyBase64: base64.StdEncoding.EncodeToString(result.Body),
		FinalURL: result.FinalURL, FinishReason: result.FinishReason, FinishDetail: result.FinishDetail, Outcome: result.Outcome,
		Usage: result.Usage, Error: failure, UsageEvent: result.UsageEvent,
	}})
}

// ClassifyAIHTTPError classifies a provider's HTTP error for a client that
// sends BuildAIRequest using its own network bridge.
func ClassifyAIHTTPError(protocol string, status int, headersJSON, bodyBase64 string) (_ string, err error) {
	defer guard(&err)
	if _, err := aiapi.NewStreamDecoder(aiapi.Protocol(protocol)); err != nil {
		return "", err
	}
	var hdr http.Header
	if strings.TrimSpace(headersJSON) != "" && json.Unmarshal([]byte(headersJSON), &hdr) != nil {
		return "", errors.New("mobile: invalid AI headers JSON")
	}
	body, err := base64.StdEncoding.DecodeString(bodyBase64)
	if err != nil {
		return "", errors.New("mobile: invalid AI error body base64")
	}
	return encode(aiapi.ClassifyHTTPResponse(aiapi.Protocol(protocol), status, hdr, body))
}

// ResolveAIRoute returns the eligible candidate IDs in caller order.
func ResolveAIRoute(inputJSON string) (_ string, err error) {
	defer guard(&err)
	var input aiapi.RouteInput
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return "", errors.New("mobile: invalid AI route input JSON")
	}
	decision, err := aiapi.ResolveRoute(context.Background(), nil, input)
	if err != nil {
		return "", err
	}
	return encode(decision)
}

// ValidateAIRouteDecision checks a mobile extension's ranking against its
// input snapshot. The client resolves credentials for the selected ID.
func ValidateAIRouteDecision(inputJSON, decisionJSON string) (_ string, err error) {
	defer guard(&err)
	var input aiapi.RouteInput
	var decision aiapi.RouteDecision
	if json.Unmarshal([]byte(inputJSON), &input) != nil || json.Unmarshal([]byte(decisionJSON), &decision) != nil {
		return "", errors.New("mobile: invalid AI route JSON")
	}
	decision, err = aiapi.ValidateRouteDecision(input, decision)
	if err != nil {
		return "", err
	}
	return encode(decision)
}

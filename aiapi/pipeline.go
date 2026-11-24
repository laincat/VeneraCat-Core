package aiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/laincat/veneracat-core/host"
)

// Extensions bundles the optional hooks used by a Pipeline. Its zero value
// sends the supplied config and native body through the host directly.
type Extensions struct {
	RoutePolicy    RoutePolicy
	UsageObservers []UsageObserver
	Masker         ContentMasker
}

// StreamTransport is the streaming counterpart of host.Transport. The client
// owns the connection. Call onHead once when metadata is available and invoke
// callbacks serially. Existing hosts may supply the head at end of body.
type StreamTransport interface {
	DoStream(context.Context, *host.Request, func(*host.Response) error, func([]byte) error) (*host.Response, error)
}

// RequestMeta carries non-secret correlation identifiers. Set Route and
// ResolveConfig together to opt in to routed execution. Only the selected
// account's credentials are resolved, by the client, for the current call.
// The native body can only be routed within its own protocol.
type RequestMeta struct {
	RequestID      string
	AccountID      string
	Provider       string
	RequireMasking bool
	Route          *RouteInput
	ResolveConfig  func(context.Context, RouteCandidate) (Config, error)
}

// CallResult reports response data and usage observed by the pipeline.
type CallResult struct {
	Status       int          `json:"status"`
	Header       http.Header  `json:"headers"`
	Body         []byte       `json:"-"`
	FinalURL     string       `json:"finalUrl"`
	Streamed     bool         `json:"streamed"`
	Text         string       `json:"text,omitempty"`
	Reasoning    string       `json:"reasoning,omitempty"`
	FinishReason string       `json:"finishReason,omitempty"`
	FinishDetail string       `json:"finishDetail,omitempty"`
	Outcome      FinishReason `json:"outcome,omitempty"`
	Usage        *TokenUsage  `json:"usage"`
	UsageEvent   *UsageEvent  `json:"usageEvent,omitempty"`
}

// Pipeline sends standard AI requests through client-owned transports.
// Configure a Pipeline before sharing it among concurrent requests.
type Pipeline struct {
	Transport       host.Transport
	StreamTransport StreamTransport
	Extensions      Extensions
	// AllowRawResponse preserves the legacy whole-response bridge, which can
	// carry any 2xx body including an SSE stream buffered by the client.
	AllowRawResponse bool
}

// ValidateExtensions checks hook versions before any request is issued.
func (p *Pipeline) ValidateExtensions() (err error) {
	defer recoverExtension("", ErrCodeInvalidRequest, &err)
	for _, obs := range p.Extensions.UsageObservers {
		if obs != nil && obs.APIVersion() != ExtensionAPIVersion {
			return &ProviderError{Code: ErrCodeInvalidRequest, Message: "incompatible usage observer apiVersion"}
		}
	}
	if p.Extensions.RoutePolicy != nil && p.Extensions.RoutePolicy.APIVersion() != ExtensionAPIVersion {
		return &ProviderError{Code: ErrCodeInvalidRequest, Message: "incompatible route policy apiVersion"}
	}
	if p.Extensions.Masker != nil && p.Extensions.Masker.APIVersion() != ExtensionAPIVersion {
		return &ProviderError{Code: ErrCodeMaskingFailed, Message: "incompatible content masker apiVersion"}
	}
	return nil
}

func (p *Pipeline) ResolveRoute(ctx context.Context, input RouteInput) (RouteDecision, error) {
	return ResolveRoute(ctx, p.Extensions.RoutePolicy, input)
}

type preparedCall struct {
	request  *host.Request
	protocol Protocol
	model    string
	meta     RequestMeta
	mask     MaskSession
}

func (p *Pipeline) prepare(ctx context.Context, cfg Config, body json.RawMessage, streamed bool, meta RequestMeta) (*preparedCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, ClassifyError(cfg.Protocol, err)
	}
	if err := p.ValidateExtensions(); err != nil {
		return nil, err
	}
	if meta.RequireMasking && p.Extensions.Masker == nil {
		return nil, &ProviderError{Code: ErrCodeMaskingFailed, Message: "required content masker is unavailable"}
	}
	if _, err := pathFor(cfg.Protocol); err != nil {
		return nil, invalidRequest(err)
	}
	if err := validateBody(cfg.Protocol, body); err != nil {
		return nil, invalidRequest(err)
	}
	if streamed {
		if err := RequireStream(body); err != nil {
			return nil, invalidRequest(err)
		}
	}
	fields := parseObject(body)
	model := jsonStringField(fields["model"])
	if meta.Route != nil {
		input := *meta.Route
		if meta.ResolveConfig == nil || (input.Protocol != "" && input.Protocol != cfg.Protocol) ||
			(input.RequestedModel != "" && input.RequestedModel != model) {
			return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "route input does not match the native request or has no config resolver"}
		}
		input.Protocol, input.RequestedModel = cfg.Protocol, model
		decision, err := p.ResolveRoute(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, candidate := range input.Candidates {
			if candidate.ID != decision.SelectedID {
				continue
			}
			cfg, err = resolveConfig(ctx, cfg.Protocol, meta.ResolveConfig, candidate)
			if err != nil {
				return nil, err
			}
			meta.AccountID, meta.Provider = candidate.AccountID, candidate.Provider
			if model != candidate.Model {
				model = candidate.Model
				fields["model"], _ = json.Marshal(model)
				body, err = json.Marshal(fields)
				if err != nil {
					return nil, safelyClassified(cfg.Protocol, 0, ErrCodeInvalidRequest)
				}
			}
			break
		}
	}
	var session MaskSession
	if p.Extensions.Masker != nil {
		var err error
		body, session, err = maskRequest(ctx, p.Extensions.Masker, cfg.Protocol, body)
		if err != nil {
			return nil, err
		}
		maskedFields := parseObject(body)
		if jsonStringField(maskedFields["model"]) != model ||
			!bytes.Equal(bytes.TrimSpace(fields["stream"]), bytes.TrimSpace(maskedFields["stream"])) {
			return nil, safelyClassified(cfg.Protocol, 0, ErrCodeMaskingFailed)
		}
		if err := validateBody(cfg.Protocol, body); err != nil {
			return nil, safelyClassified(cfg.Protocol, 0, ErrCodeMaskingFailed)
		}
	}
	req, err := buildRequest(cfg, body)
	if err != nil {
		return nil, invalidRequest(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, ClassifyError(cfg.Protocol, err)
	}
	return &preparedCall{request: req, protocol: cfg.Protocol, model: model, meta: meta, mask: session}, nil
}

func resolveConfig(ctx context.Context, protocol Protocol, resolve func(context.Context, RouteCandidate) (Config, error), candidate RouteCandidate) (cfg Config, err error) {
	defer recoverExtension(protocol, ErrCodeInvalidRequest, &err)
	cfg, err = resolve(ctx, candidate)
	if ctx.Err() != nil {
		return Config{}, ClassifyError(protocol, ctx.Err())
	}
	if err != nil || cfg.Protocol != protocol {
		return Config{}, &ProviderError{Code: ErrCodeInvalidRequest, Protocol: protocol, Message: "could not resolve the selected account for the requested protocol"}
	}
	return cfg, nil
}

func maskRequest(ctx context.Context, masker ContentMasker, protocol Protocol, body json.RawMessage) (masked json.RawMessage, session MaskSession, err error) {
	defer recoverExtension(protocol, ErrCodeMaskingFailed, &err)
	masked, session, err = masker.MaskRequest(ctx, protocol, bytes.Clone(body))
	if ctx.Err() != nil {
		return nil, nil, ClassifyError(protocol, ctx.Err())
	}
	if err != nil || session == nil {
		return nil, nil, safelyClassified(protocol, 0, ErrCodeMaskingFailed)
	}
	return masked, session, nil
}

func restoreBody(call *preparedCall, body []byte) (restored []byte, err error) {
	defer recoverExtension(call.protocol, ErrCodeMaskingFailed, &err)
	restored, err = call.mask.RestoreBody(json.RawMessage(bytes.Clone(body)))
	if err != nil || parseObject(restored) == nil {
		return nil, safelyClassified(call.protocol, 0, ErrCodeMaskingFailed)
	}
	return restored, nil
}

func restoreDelta(call *preparedCall, kind StreamEventKind, text string, final bool) (restored string, err error) {
	defer recoverExtension(call.protocol, ErrCodeMaskingFailed, &err)
	restored, err = call.mask.RestoreDelta(kind, text, final)
	if err != nil {
		return "", safelyClassified(call.protocol, 0, ErrCodeMaskingFailed)
	}
	return restored, nil
}

func (p *Pipeline) observe(ctx context.Context, call *preparedCall, res *CallResult, err error) {
	usage := res.Usage.Clone()
	if usage != nil {
		usage.Raw = nil
	}
	event := UsageEvent{APIVersion: ExtensionAPIVersion, RequestID: call.meta.RequestID, AccountID: call.meta.AccountID,
		Provider: call.meta.Provider, Model: call.model, Protocol: call.protocol, Status: res.Status, Streamed: res.Streamed,
		Usage: usage, Error: ClassifyError(call.protocol, err)}
	switch res.Outcome {
	case FinishStop, FinishLength, FinishContentFilter, FinishToolCalls, FinishError, FinishCancelled, FinishInProgress:
		event.Outcome = res.Outcome
	}
	res.UsageEvent = &event
	for _, obs := range p.Extensions.UsageObservers {
		if obs != nil {
			observeUsage(ctx, obs, event)
		}
	}
}

func observeUsage(ctx context.Context, obs UsageObserver, event UsageEvent) {
	defer func() { _ = recover() }()
	event.Usage = event.Usage.Clone()
	event.Error = event.Error.clone()
	obs.OnUsage(ctx, event)
}

func transportError(ctx context.Context, protocol Protocol, err error) *ProviderError {
	if ctx.Err() != nil {
		return ClassifyError(protocol, ctx.Err())
	}
	if err != nil {
		classified := ClassifyError(protocol, err)
		if classified.Code != ErrCodeUnknown {
			return classified
		}
	}
	return safelyClassified(protocol, 0, ErrCodeUpstream)
}

// Do sends a whole-response call. On failure the Go result can still carry
// status and usage. An HTTP or provider error body is excluded from the result.
func (p *Pipeline) Do(ctx context.Context, cfg Config, body json.RawMessage, meta ...RequestMeta) (res *CallResult, err error) {
	if p.Transport == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "AI transport is required"}
	}
	call, err := p.prepare(ctx, cfg, body, false, firstMeta(meta))
	if err != nil {
		return nil, err
	}
	res = &CallResult{}
	defer func() { p.observe(ctx, call, res, err) }()
	response, err := p.Transport.Do(ctx, call.request)
	if err != nil || response == nil || ctx.Err() != nil {
		return res, transportError(ctx, call.protocol, err)
	}
	res.Status, res.Header, res.FinalURL = response.Status, response.Header, response.FinalURL
	if !success(res.Status) {
		return res, ClassifyHTTPResponse(call.protocol, res.Status, res.Header, response.Body)
	}
	summary, summaryErr := ParseResponse(call.protocol, response.Body)
	if summary != nil {
		res.Usage, res.Outcome = summary.Usage, summary.Outcome
		res.FinishReason, res.FinishDetail = summary.FinishReason, summary.FinishDetail
	}
	if summaryErr != nil && (call.mask != nil || !p.AllowRawResponse || !isInvalidFormat(summaryErr)) {
		return res, summaryErr
	}
	if call.mask != nil {
		res.Body, err = restoreBody(call, response.Body)
		if err != nil {
			return res, err
		}
		summary, err = ParseResponse(call.protocol, res.Body)
		if err != nil {
			res.Body = nil
			return res, safelyClassified(call.protocol, res.Status, ErrCodeMaskingFailed)
		}
	} else {
		res.Body = response.Body
	}
	if summary != nil {
		res.Text, res.Reasoning = summary.Text, summary.Reasoning
	}
	return res, nil
}

// DoStream sends a streamed call. The optional callback receives decoded
// events only after HTTP success is known. A transport EOF without a protocol
// completion marker fails the request.
func (p *Pipeline) DoStream(ctx context.Context, cfg Config, body json.RawMessage, onEvent func(StreamEvent) error, meta ...RequestMeta) (res *CallResult, err error) {
	if p.StreamTransport == nil {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "this client cannot stream"}
	}
	call, err := p.prepare(ctx, cfg, body, true, firstMeta(meta))
	if err != nil {
		return nil, err
	}
	call.request.Header.Set("Accept", "text/event-stream")
	decoder, _ := NewStreamDecoder(call.protocol)
	res = &CallResult{Streamed: true}
	defer func() {
		res.FinishReason, res.FinishDetail = decoder.FinishReason(), decoder.FinishDetail()
		res.Outcome, res.Usage = decoder.Outcome(), decoder.Usage()
		p.observe(ctx, call, res, err)
	}()

	var text, reasoning strings.Builder
	emit := func(event StreamEvent) error {
		switch event.Kind {
		case StreamEventText:
			text.WriteString(event.Text)
		case StreamEventReasoning:
			reasoning.WriteString(event.Text)
		}
		if onEvent != nil {
			return onEvent(event)
		}
		return nil
	}
	deliver := func(event StreamEvent) error {
		if call.mask != nil {
			switch event.Kind {
			case StreamEventText, StreamEventReasoning:
				restored, err := restoreDelta(call, event.Kind, event.Text, false)
				if err != nil || restored == "" {
					return err
				}
				event.Text = restored
			case StreamEventDone:
				for _, kind := range []StreamEventKind{StreamEventReasoning, StreamEventText} {
					tail, err := restoreDelta(call, kind, "", true)
					if err != nil {
						return err
					}
					if tail != "" {
						if err := emit(StreamEvent{Kind: kind, Text: tail}); err != nil {
							return err
						}
					}
				}
			}
		}
		providerErr := event.Error.clone()
		if err := emit(event); err != nil {
			return err
		}
		if providerErr != nil {
			return providerErr
		}
		return nil
	}

	// Legacy bridges may announce headers after body chunks. Buffer these
	// chunks until we can distinguish a provider error body from a reply.
	const maxPending = 64 << 20
	const maxErrorBody = 64 << 10
	var pending, errorBody []byte
	var callbackErr error
	onChunk := func(chunk []byte) error {
		if res.Status == 0 {
			if len(chunk) > maxPending-len(pending) {
				callbackErr = safelyClassified(call.protocol, 0, ErrCodeInvalidFormat)
				return callbackErr
			}
			pending = append(pending, chunk...)
			return nil
		}
		if !success(res.Status) {
			errorBody = append(errorBody, chunk[:min(len(chunk), maxErrorBody-len(errorBody))]...)
			return nil
		}
		events, decodeErr := decoder.PushEvents(chunk)
		for _, event := range events {
			if err := deliver(event); err != nil {
				callbackErr = err
				return err
			}
		}
		callbackErr = decodeErr
		return decodeErr
	}
	onHead := func(head *host.Response) error {
		res.Status, res.Header, res.FinalURL = head.Status, head.Header, head.FinalURL
		buffered := pending
		pending = nil
		return onChunk(buffered)
	}
	response, sendErr := p.StreamTransport.DoStream(ctx, call.request, onHead, onChunk)
	if res.Status == 0 && response != nil && response.Status != 0 && callbackErr == nil {
		_ = onHead(response)
	}
	switch {
	case ctx.Err() != nil:
		return res, ClassifyError(call.protocol, ctx.Err())
	case res.Status != 0 && !success(res.Status):
		if len(errorBody) == 0 && response != nil {
			errorBody = response.Body
		}
		return res, ClassifyHTTPResponse(call.protocol, res.Status, res.Header, errorBody)
	case callbackErr != nil:
		return res, callbackErr
	case sendErr != nil || res.Status == 0:
		return res, transportError(ctx, call.protocol, sendErr)
	case !decoder.Done():
		return res, &ProviderError{Code: ErrCodeUpstream, Protocol: call.protocol, Status: res.Status, Retryable: true, Message: "AI stream ended before completion"}
	}
	res.Text, res.Reasoning = text.String(), reasoning.String()
	return res, nil
}

func invalidRequest(err error) *ProviderError {
	return &ProviderError{Code: ErrCodeInvalidRequest, Message: err.Error()}
}

func isInvalidFormat(err error) bool {
	var provider *ProviderError
	return errors.As(err, &provider) && provider.Code == ErrCodeInvalidFormat
}

func success(status int) bool { return status >= 200 && status < 300 }

func firstMeta(meta []RequestMeta) RequestMeta {
	if len(meta) > 0 {
		return meta[0]
	}
	return RequestMeta{}
}

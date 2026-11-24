package aiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/laincat/veneracat-core/host"
)

// ErrorCode is shared by standard AI protocols and Magpie provider plugins.
type ErrorCode string

const (
	ErrCodeAuth           ErrorCode = "auth_failed"
	ErrCodeRateLimit      ErrorCode = "rate_limit"
	ErrCodeQuotaExhausted ErrorCode = "quota_exhausted"
	ErrCodeContentBlocked ErrorCode = "content_blocked"
	ErrCodeUpstream       ErrorCode = "upstream_error"
	ErrCodeCancelled      ErrorCode = "cancelled"
	ErrCodeInvalidRequest ErrorCode = "invalid_request"
	ErrCodeInvalidFormat  ErrorCode = "invalid_format"
	ErrCodeMaskingFailed  ErrorCode = "masking_failed"
	ErrCodeUnknown        ErrorCode = "unknown"
)

// ProviderError contains safe diagnostics. RetryAfter is a whole-second delay.
// Cause is limited to local sentinels such as context.Canceled by Core.
type ProviderError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	Status     int       `json:"status,omitempty"`
	Retryable  bool      `json:"retryable"`
	RetryAfter *int64    `json:"retryAfter,omitempty"`
	Protocol   Protocol  `json:"protocol,omitempty"`
	cause      error
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("aiapi: %s (HTTP %d): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("aiapi: %s: %s", e.Code, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.cause }

func (e *ProviderError) clone() *ProviderError {
	if e == nil {
		return nil
	}
	out := *e
	if e.RetryAfter != nil {
		value := *e.RetryAfter
		out.RetryAfter = &value
	}
	return &out
}

// RetryAfterDuration reports the provider's suggested delay if representable.
func (e *ProviderError) RetryAfterDuration() (time.Duration, bool) {
	if e.RetryAfter == nil || *e.RetryAfter < 0 || *e.RetryAfter > int64((1<<63-1)/time.Second) {
		return 0, false
	}
	return time.Duration(*e.RetryAfter) * time.Second, true
}

func safelyClassified(protocol Protocol, status int, code ErrorCode) *ProviderError {
	err := &ProviderError{Status: status, Code: code}
	if protocol == Chat || protocol == Responses || protocol == Messages {
		err.Protocol = protocol
	}
	switch code {
	case ErrCodeAuth:
		err.Message = "invalid API key or insufficient permissions"
	case ErrCodeRateLimit:
		err.Message, err.Retryable = "provider rate limited the request", true
	case ErrCodeQuotaExhausted:
		err.Message = "provider quota exhausted"
	case ErrCodeContentBlocked:
		err.Message = "provider blocked the content"
	case ErrCodeUpstream:
		err.Message, err.Retryable = "upstream provider request failed", true
	case ErrCodeCancelled:
		err.Message = "request cancelled"
	case ErrCodeInvalidRequest:
		err.Message = "invalid AI request"
	case ErrCodeInvalidFormat:
		err.Message = "invalid AI response format"
	case ErrCodeMaskingFailed:
		err.Message = "content masking or restoration failed"
	default:
		err.Code, err.Message = ErrCodeUnknown, "provider request failed"
	}
	return err
}

// ClassifyError classifies a transport or plugin failure. External text is
// used for classification only, since it can contain arbitrary credentials.
func ClassifyError(protocol Protocol, err error) *ProviderError {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			out := safelyClassified(protocol, 0, ErrCodeCancelled)
			out.cause = sentinel
			if sentinel == context.DeadlineExceeded {
				out.Message, out.Retryable = "request deadline exceeded", true
			}
			return out
		}
	}
	var existing *ProviderError
	if errors.As(err, &existing) {
		// Keep only classified values when an error crosses the host boundary.
		out := safelyClassified(protocol, existing.Status, existing.Code)
		if out.Protocol == "" && (existing.Protocol == Chat || existing.Protocol == Responses || existing.Protocol == Messages) {
			out.Protocol = existing.Protocol
		}
		out.RetryAfter = existing.clone().RetryAfter
		return out
	}
	return safelyClassified(protocol, 0, classifyCategory(0, "", "", err.Error()))
}

// ClassifyHTTPResponse classifies an HTTP failure without retaining its body,
// message, headers or provider-supplied error code.
func ClassifyHTTPResponse(protocol Protocol, status int, headers http.Header, body []byte) *ProviderError {
	return classifyHTTPResponseAt(protocol, status, headers, body, time.Now())
}

func classifyHTTPResponseAt(protocol Protocol, status int, headers http.Header, body []byte, now time.Time) *ProviderError {
	out := safelyClassified(protocol, status, errorCategory(status, body))
	out.RetryAfter = parseRetryAfter(headers, now)
	return out
}

// ClassifyStreamError classifies an SSE provider error envelope or error object.
func ClassifyStreamError(protocol Protocol, raw json.RawMessage) *ProviderError {
	out := safelyClassified(protocol, 0, errorCategory(0, raw))
	fields := parseObject(raw)
	if inner := parseObject(fields["error"]); inner != nil {
		fields = inner
	}
	if count := parseJSONNumber(fields["retry_after"]); count != nil {
		if seconds, err := count.Int64(); err == nil {
			out.RetryAfter = &seconds
		}
	}
	return out
}

func parseRetryAfter(headers http.Header, now time.Time) *int64 {
	value := strings.TrimSpace(host.HeaderValue(headers, "Retry-After"))
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil && secs >= 0 {
		return &secs
	}
	if until, err := http.ParseTime(value); err == nil {
		delay := max(0, until.Sub(now))
		secs := int64(delay / time.Second)
		if delay%time.Second != 0 {
			secs++
		}
		return &secs
	}
	value = strings.TrimSpace(host.HeaderValue(headers, "retry-after-ms"))
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil && ms >= 0 {
		secs := ms / 1000
		if ms%1000 != 0 {
			secs++
		}
		return &secs
	}
	return nil
}

func errorCategory(status int, raw []byte) ErrorCode {
	obj := parseObject(raw)
	if inner := parseObject(obj["error"]); inner != nil {
		obj = inner
	}
	message := jsonStringField(obj["message"])
	if message == "" {
		message = jsonStringField(obj["error"])
	}
	if obj == nil {
		message = string(raw)
	}
	return classifyCategory(status, jsonStringField(obj["type"]), jsonStringField(obj["code"]), message)
}

func jsonStringField(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func classifyCategory(status int, kind, code, message string) ErrorCode {
	// Specific provider codes take precedence over broad status/type values.
	for _, value := range []string{code, kind} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "content_policy_violation", "content_filter", "content_blocked", "moderation", "refusal":
			return ErrCodeContentBlocked
		case "insufficient_quota", "quota_exhausted", "quota_exceeded", "billing_hard_limit_reached", "credit_balance_too_low", "insufficient_balance":
			return ErrCodeQuotaExhausted
		case "cancelled", "canceled", "request_cancelled", "aborted":
			return ErrCodeCancelled
		case "rate_limit", "rate_limit_error", "rate_limit_exceeded", "too_many_requests":
			return ErrCodeRateLimit
		case "auth", "authentication_error", "invalid_api_key", "permission_error", "unauthorized", "forbidden", "expired_token":
			return ErrCodeAuth
		case "server_error", "api_error", "overloaded_error", "service_unavailable", "internal_error":
			return ErrCodeUpstream
		}
	}
	message = strings.ToLower(message)
	switch {
	case status == 402, strings.Contains(message, "credit balance is too low"), strings.Contains(message, "exceeded your current quota"):
		return ErrCodeQuotaExhausted
	case strings.Contains(message, "content policy"), strings.Contains(message, "content filter"):
		return ErrCodeContentBlocked
	case status == 429, strings.Contains(message, "rate limit"):
		return ErrCodeRateLimit
	case status == 401 || status == 403, strings.Contains(message, "invalid api key"):
		return ErrCodeAuth
	case status >= 500 || status == 408:
		return ErrCodeUpstream
	case status >= 400, kind == "invalid_request_error", code == "invalid_request" || code == "model_not_found" || code == "context_length_exceeded":
		return ErrCodeInvalidRequest
	default:
		return ErrCodeUnknown
	}
}

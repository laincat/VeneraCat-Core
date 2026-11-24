package aiapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClassifyHTTPResponseCategories(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name           string
		protocol       Protocol
		status         int
		headers        http.Header
		body           string
		wantCode       ErrorCode
		wantRetryable  bool
		wantRetryAfter *int64
	}{
		{
			name:          "openai auth 401",
			protocol:      Chat,
			status:        http.StatusUnauthorized,
			body:          `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
			wantCode:      ErrCodeAuth,
			wantRetryable: false,
		},
		{
			name:          "anthropic auth 403",
			protocol:      Messages,
			status:        http.StatusForbidden,
			body:          `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`,
			wantCode:      ErrCodeAuth,
			wantRetryable: false,
		},
		{
			name:           "rate limit with retry-after seconds",
			protocol:       Chat,
			status:         http.StatusTooManyRequests,
			headers:        http.Header{"Retry-After": {"15"}},
			body:           `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`,
			wantCode:       ErrCodeRateLimit,
			wantRetryable:  true,
			wantRetryAfter: ptrInt64(15),
		},
		{
			name:           "rate limit with retry-after-ms",
			protocol:       Responses,
			status:         http.StatusTooManyRequests,
			headers:        http.Header{"retry-after-ms": {"1500"}},
			body:           `{"error":{"message":"Too many requests","type":"rate_limit_exceeded"}}`,
			wantCode:       ErrCodeRateLimit,
			wantRetryable:  true,
			wantRetryAfter: ptrInt64(2),
		},
		{
			name:           "rate limit with HTTP date",
			protocol:       Messages,
			status:         http.StatusTooManyRequests,
			headers:        http.Header{"Retry-After": {now.Add(42 * time.Second).Format(http.TimeFormat)}},
			body:           `{"error":{"type":"rate_limit_error","message":"slow down"}}`,
			wantCode:       ErrCodeRateLimit,
			wantRetryable:  true,
			wantRetryAfter: ptrInt64(42),
		},
		{
			name:          "429 insufficient quota is quota_exhausted not retryable",
			protocol:      Chat,
			status:        http.StatusTooManyRequests,
			body:          `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`,
			wantCode:      ErrCodeQuotaExhausted,
			wantRetryable: false,
		},
		{
			name:          "402 billing balance depleted",
			protocol:      Messages,
			status:        http.StatusPaymentRequired,
			body:          `{"error":{"message":"Your credit balance is too low"}}`,
			wantCode:      ErrCodeQuotaExhausted,
			wantRetryable: false,
		},
		{
			name:          "400 content policy violation is content_blocked",
			protocol:      Chat,
			status:        http.StatusBadRequest,
			body:          `{"error":{"message":"Rejected by safety system","code":"content_policy_violation"}}`,
			wantCode:      ErrCodeContentBlocked,
			wantRetryable: false,
		},
		{
			name:          "400 invalid request",
			protocol:      Messages,
			status:        http.StatusBadRequest,
			body:          `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is invalid"}}`,
			wantCode:      ErrCodeInvalidRequest,
			wantRetryable: false,
		},
		{
			name:           "529 anthropic overloaded",
			protocol:       Messages,
			status:         529,
			headers:        http.Header{"Retry-After": {"7"}},
			body:           `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantCode:       ErrCodeUpstream,
			wantRetryable:  true,
			wantRetryAfter: ptrInt64(7),
		},
		{
			name:          "503 upstream",
			protocol:      Chat,
			status:        http.StatusServiceUnavailable,
			body:          `service unavailable`,
			wantCode:      ErrCodeUpstream,
			wantRetryable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := classifyHTTPResponseAt(tc.protocol, tc.status, tc.headers, []byte(tc.body), now)
			if pe.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", pe.Code, tc.wantCode, pe)
			}
			if pe.Retryable != tc.wantRetryable {
				t.Fatalf("retryable = %v, want %v", pe.Retryable, tc.wantRetryable)
			}
			if tc.wantRetryAfter == nil {
				if pe.RetryAfter != nil {
					t.Fatalf("retryAfter = %d, want nil", *pe.RetryAfter)
				}
			} else {
				if pe.RetryAfter == nil || *pe.RetryAfter != *tc.wantRetryAfter {
					t.Fatalf("retryAfter = %v, want %d", pe.RetryAfter, *tc.wantRetryAfter)
				}
				dur, ok := pe.RetryAfterDuration()
				if !ok || dur != time.Duration(*tc.wantRetryAfter)*time.Second {
					t.Fatalf("RetryAfterDuration = (%v, %v)", dur, ok)
				}
			}
		})
	}
}

func TestClassifyErrorCancellationAndUnwrap(t *testing.T) {
	canceled := ClassifyError(Chat, context.Canceled)
	if canceled.Code != ErrCodeCancelled || canceled.Retryable {
		t.Fatalf("canceled = %+v", canceled)
	}
	if !errors.Is(canceled, context.Canceled) {
		t.Fatal("ProviderError did not preserve context.Canceled via Unwrap")
	}

	deadline := ClassifyError(Responses, context.DeadlineExceeded)
	if deadline.Code != ErrCodeCancelled || !deadline.Retryable {
		t.Fatalf("deadline = %+v", deadline)
	}
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("ProviderError did not preserve context.DeadlineExceeded via Unwrap")
	}
}

func TestSanitizeTextAndErrorsNeverLeakSecrets(t *testing.T) {
	secret := "mock-key-that-has-no-known-prefix"
	raw := "failed with Authorization: Bearer supersecret12345; x-api-key: " + secret +
		"; Cookie: session=abc123; Set-Cookie: sid=xyz; mock-extra-key; https://api.test/v1?api_key=querysecret99"
	pe := ClassifyHTTPResponse(Chat, http.StatusUnauthorized, nil, []byte(`{"error":{"message":`+mustJSONString(t, raw)+`}}`))

	formatted := pe.Error()
	for _, leaked := range []string{secret, "supersecret12345", "session=abc123", "sid=xyz", "mock-extra-key", "querysecret99"} {
		if strings.Contains(formatted, leaked) {
			t.Fatalf("error leaked %q: %s", leaked, formatted)
		}
		if strings.Contains(pe.Message, leaked) {
			t.Fatalf("message leaked %q: %s", leaked, pe.Message)
		}
	}
	if pe.Code != ErrCodeAuth {
		t.Fatalf("code = %q, want %q", pe.Code, ErrCodeAuth)
	}
}

func TestClassifyStreamErrorSanitizesAndClassifies(t *testing.T) {
	pe := ClassifyStreamError(Messages, json.RawMessage(`{"type":"rate_limit_error","message":"Bearer tok_secret_value hit rate limit"}`))
	if pe.Code != ErrCodeRateLimit || !pe.Retryable {
		t.Fatalf("pe = %+v", pe)
	}
	if strings.Contains(pe.Message, "tok_secret_value") || strings.Contains(pe.Error(), "tok_secret_value") {
		t.Fatalf("stream error leaked secret: %+v", pe)
	}
}

func ptrInt64(v int64) *int64 { return &v }

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

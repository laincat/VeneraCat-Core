package gohost

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
)

// SetAIExtensions configures optional versioned AI extension hooks (routing
// policy, usage observers, and content masker) on Core. Passing the zero value
// restores the direct zero-policy baseline.
func (c *Core) SetAIExtensions(ext aiapi.Extensions) error {
	if c == nil {
		return errors.New("gohost: core is nil")
	}
	check := &aiapi.Pipeline{Extensions: ext}
	if err := check.ValidateExtensions(); err != nil {
		return err
	}
	cloned := aiapi.Extensions{
		RoutePolicy:    ext.RoutePolicy,
		Masker:         ext.Masker,
		UsageObservers: append([]aiapi.UsageObserver(nil), ext.UsageObservers...),
	}
	c.aiExtMu.Lock()
	c.aiExt = cloned
	c.aiExtMu.Unlock()
	return nil
}

// AIExtensions returns a shallow copy of the currently registered AI extensions.
func (c *Core) AIExtensions() aiapi.Extensions {
	if c == nil {
		return aiapi.Extensions{}
	}
	c.aiExtMu.RLock()
	ext := c.aiExt
	ext.UsageObservers = append([]aiapi.UsageObserver(nil), c.aiExt.UsageObservers...)
	c.aiExtMu.RUnlock()
	return ext
}

// AIPipeline returns an *aiapi.Pipeline bound to Core's host.Transport and
// streaming transport (both strictly enforcing Cookies: false). When ext is
// omitted, Core's registered extensions (zero-policy by default) are used.
func (c *Core) AIPipeline(ext ...aiapi.Extensions) *aiapi.Pipeline {
	var selected aiapi.Extensions
	if len(ext) > 0 {
		selected = ext[0]
	} else {
		selected = c.AIExtensions()
	}
	var tr host.Transport
	var str aiapi.StreamTransport
	if c != nil && c.tr != nil {
		tr = c.tr
		str = &transportStream{tr: c.tr}
	}
	return &aiapi.Pipeline{
		Transport:       tr,
		StreamTransport: str,
		Extensions:      selected,
	}
}

// ResolveAIRoute evaluates input using Core's registered RoutePolicy, or the
// deterministic zero-policy baseline when no policy is registered.
func (c *Core) ResolveAIRoute(ctx context.Context, input aiapi.RouteInput) (aiapi.RouteDecision, error) {
	return c.AIPipeline().ResolveRoute(ctx, input)
}

// SendAI performs a whole-response standard AI request through Core's
// host.Transport (with Cookies: false) and any registered extensions.
func (c *Core) SendAI(ctx context.Context, cfg aiapi.Config, body json.RawMessage, meta ...aiapi.RequestMeta) (*aiapi.CallResult, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	return c.AIPipeline().Do(ctx, cfg, body, meta...)
}

// SendAIStream performs a streamed standard AI request through Core's
// Transport.OpenStream (with Cookies: false), decoding native structured
// events and invoking any registered masking or usage observation hooks.
func (c *Core) SendAIStream(ctx context.Context, cfg aiapi.Config, body json.RawMessage, onEvent func(aiapi.StreamEvent) error, meta ...aiapi.RequestMeta) (*aiapi.CallResult, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	return c.AIPipeline().DoStream(ctx, cfg, body, onEvent, meta...)
}

// transportStream adapts gohost.Transport.OpenStream to aiapi.StreamTransport
// without opening any separate HTTP client or direct connection.
type transportStream struct {
	tr *Transport
}

func (s *transportStream) DoStream(ctx context.Context, req *host.Request, onHead func(*host.Response) error, onChunk func([]byte) error) (*host.Response, error) {
	if s == nil || s.tr == nil {
		return nil, errors.New("gohost: transport is not initialized")
	}
	copied := *req
	copied.Cookies = false
	resp, err := s.tr.OpenStream(ctx, &copied)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	finalURL := copied.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	out := &host.Response{
		Status:   resp.StatusCode,
		Header:   resp.Header.Clone(),
		FinalURL: finalURL,
	}

	if err := onHead(out); err != nil {
		return out, err
	}
	buf := make([]byte, 16*1024)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 && onChunk != nil {
			if err := onChunk(buf[:n]); err != nil {
				return out, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return out, nil
		}
		if readErr != nil {
			return out, readErr
		}
	}
}

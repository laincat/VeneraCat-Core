// Sending a conversation through the package that owns the vendor.
//
// Magpie's provider plugins do not hand over headers: their loader refreshes
// the account and returns a `fetch` that carries the sign-in, so the request
// has to go through the package. This calls that fetch and reads the reply as
// it arrives, without the client learning the vendor's shape.
package magpieplugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host/sse"
)

// Chat sends one conversation turn through the package's loader and reports the
// reply as stream events.
//
// Only the Chat Completions dialect is driven this way: it is what an
// OpenAI-compatible provider package serves, and Core says so plainly instead
// of guessing a vendor's URL. A package that does not offer a loader is refused
// with its own reason.
// pathForDialect is where a package's base URL expects a chat request, per
// dialect. A vendor that uses another path declares it in its loader's `path`.
func pathForDialect(dialect Dialect) (string, error) {
	switch dialect {
	case DialectChat:
		return "/chat/completions", nil
	case DialectMessages:
		return "/messages", nil
	case DialectResponses:
		return "/responses", nil
	default:
		return "", errors.New("magpieplugin: unsupported chat dialect")
	}
}

// Chat streams a provider's reply. Cancellation aborts the instance's pending
// fetches. Use a separate instance for a chat that may be cancelled.
func (in *Instance) Chat(ctx context.Context, req *ChatRequest, onEvent func(Event) error) error {
	if err := ctx.Err(); err != nil {
		return aiapi.ClassifyError("", err)
	}
	if req == nil {
		return &ProviderError{Code: aiapi.ErrCodeInvalidRequest, Message: "chat request is required"}
	}
	stop := context.AfterFunc(ctx, in.cancel)
	defer stop()
	if onEvent == nil {
		onEvent = func(Event) error { return nil }
	}
	prepared, err := in.hook("chatPrepare", "{}")
	if err != nil {
		return pluginCallError(ctx, "", err)
	}
	plan, ok := prepared.(map[string]any)
	if !ok || plan["failure"] != nil {
		return invalidChatPlan(plan)
	}
	base, _ := plan["baseURL"].(string)
	if base == "" {
		return invalidChatPlan(plan)
	}
	dialect := DialectChat
	if declared, ok := plan["dialect"].(string); ok && declared != "" {
		dialect = Dialect(declared)
	}
	path, err := pathForDialect(dialect)
	if err != nil {
		return invalidChatPlan(plan)
	}
	protocol := aiapi.Protocol(dialect)
	body, err := req.Body(dialect)
	if err != nil {
		return &ProviderError{Code: aiapi.ErrCodeInvalidRequest, Protocol: protocol, Message: "invalid chat request body"}
	}
	url := strings.TrimSuffix(base, "/") + path

	buffer := newReplyBuffer()
	handle := in.openReply(buffer)
	defer in.closeReply(handle)

	var callbackErr, streamErr error
	finished := false
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader, err := NewStreamReader(buffer, dialect, 0)
		if err != nil {
			streamErr = err
			buffer.CloseWithError(err)
			return
		}
		for {
			event, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				streamErr = err
				// Let the package's fetch fail rather than hang on a write.
				buffer.CloseWithError(err)
				return
			}
			if event.Kind == "" {
				continue
			}
			if err := onEvent(event); err != nil {
				callbackErr = err
				buffer.CloseWithError(err)
				return
			}
			finished = event.Kind == EventDone
			if event.Error != nil {
				streamErr = event.Error
				buffer.CloseWithError(event.Error)
				return
			}
		}
	}()

	arg, _ := json.Marshal(map[string]any{"handle": handle, "url": url, "body": string(body)})
	answer, runErr := in.hook("chatRun", string(arg))
	// The package's fetch has returned; nothing more will be pushed.
	buffer.Close()
	<-readDone
	if err := ctx.Err(); err != nil {
		return aiapi.ClassifyError(protocol, err)
	}
	if callbackErr != nil {
		return callbackErr
	}
	if streamErr != nil {
		if errors.Is(streamErr, sse.ErrEventTooLarge) {
			return &ProviderError{Code: aiapi.ErrCodeInvalidFormat, Protocol: protocol, Message: "stream event exceeds the size limit"}
		}
		return aiapi.ClassifyError(protocol, streamErr)
	}
	if runErr != nil {
		return pluginCallError(ctx, protocol, runErr)
	}
	result, _ := answer.(map[string]any)
	if ok, _ := result["ok"].(bool); !ok {
		return pluginHTTPError(protocol, result)
	}
	if !finished {
		return &ProviderError{Code: aiapi.ErrCodeUpstream, Protocol: protocol, Retryable: true, Message: "AI stream ended before completion"}
	}
	return nil
}

func invalidChatPlan(plan map[string]any) error {
	message := "the package returned no usable request entry"
	if plan["failure"] == "no_loader" {
		message = "the package declares no loader"
	}
	return &ProviderError{Code: aiapi.ErrCodeInvalidRequest, Message: message}
}

func pluginCallError(ctx context.Context, protocol aiapi.Protocol, err error) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return aiapi.ClassifyError(protocol, err)
}

func pluginHTTPError(protocol aiapi.Protocol, result map[string]any) *ProviderError {
	status, _ := result["status"].(int64)
	body, _ := result["body"].(string)
	header := http.Header{}
	if value, ok := result["retryAfter"].(string); ok {
		header.Set("Retry-After", value)
	}
	if value, ok := result["retryAfterMs"].(string); ok {
		header.Set("retry-after-ms", value)
	}
	return aiapi.ClassifyHTTPResponse(protocol, int(status), header, []byte(body))
}

// openReply reserves a handle for one reply and remembers where its text goes.
func (in *Instance) openReply(buffer *replyBuffer) int64 {
	in.chatMu.Lock()
	defer in.chatMu.Unlock()
	if in.chatPipes == nil {
		in.chatPipes = map[int64]*replyBuffer{}
	}
	in.chatNext++
	handle := in.chatNext
	in.chatPipes[handle] = buffer
	return handle
}

func (in *Instance) closeReply(handle int64) {
	in.chatMu.Lock()
	buffer := in.chatPipes[handle]
	delete(in.chatPipes, handle)
	in.chatMu.Unlock()
	if buffer != nil {
		buffer.Close()
	}
}

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
	"fmt"
	"io"
	"strings"
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
		return "", fmt.Errorf("magpieplugin: %q replies are not supported yet", dialect)
	}
}

func (in *Instance) Chat(ctx context.Context, req *ChatRequest, onEvent func(Event) error) error {
	if onEvent == nil {
		onEvent = func(Event) error { return nil }
	}
	prepared, err := in.hook("chatPrepare", "{}")
	if err != nil {
		return err
	}
	plan, ok := prepared.(map[string]any)
	if !ok {
		return errors.New("magpieplugin: the package prepared no request")
	}
	base, _ := plan["baseURL"].(string)
	if base == "" {
		return errors.New("magpieplugin: the package named no base URL")
	}
	dialect := DialectChat
	if declared, ok := plan["dialect"].(string); ok && declared != "" {
		dialect = Dialect(declared)
	}
	path, err := pathForDialect(dialect)
	if err != nil {
		return err
	}
	body, err := req.Body(dialect)
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(base, "/") + path

	buffer := newReplyBuffer()
	handle := in.openReply(buffer)
	defer in.closeReply(handle)

	var callbackErr error
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader, err := NewStreamReader(buffer, dialect, 0)
		if err != nil {
			callbackErr = err
			buffer.CloseWithError(err)
			return
		}
		for {
			event, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				callbackErr = err
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
		}
	}()

	arg, err := json.Marshal(map[string]any{"handle": handle, "url": url, "body": string(body)})
	if err != nil {
		return err
	}
	answer, runErr := in.hook("chatRun", string(arg))
	// The package's fetch has returned; nothing more will be pushed.
	buffer.Close()
	<-readDone
	if callbackErr != nil {
		return callbackErr
	}
	if runErr != nil {
		return runErr
	}
	if result, ok := answer.(map[string]any); ok {
		if okFlag, isBool := result["ok"].(bool); isBool && !okFlag {
			status, _ := result["status"].(int64)
			text, _ := result["body"].(string)
			return fmt.Errorf("magpieplugin: %s answered HTTP %d: %s", base, status, text)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return nil
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

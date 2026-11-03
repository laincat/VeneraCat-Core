package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/laincat/veneracat-core/magpieplugin"
)

// HostStream is the optional half of a client's transport: the part that can
// hand a reply over while it is still arriving.
//
// It is a separate interface on purpose. Adding methods to Host would break
// every client that already conforms to it, and a client that cannot stream
// should keep working with whole replies rather than fail to build. A client
// opts in by passing this to NewWithStream or NewMagpieWithStream.
//
// The protocol is a poll, because gomobile cannot call back into the client
// from Go: StreamOpen returns an id, StreamNext returns one piece at a time,
// and StreamClose releases it. Each call is blocking on the client's side, and
// a bodyless chunk may announce response headers or the end of the stream.
type HostStream interface {
	// StreamOpen sends the request and returns {"id": n}. It may also return
	// status, headers and finalUrl once the response head is available.
	// The client must not wait for the body to finish.
	StreamOpen(requestJSON string) (string, error)
	// StreamNext waits for the next piece and returns
	// {"done":bool,"chunkBase64":"…","status":n,"headers":{…},"finalUrl":"…"}.
	// For early fetch resolution, send status/headers/finalUrl before or with
	// the first body chunk (a head-only chunk is allowed). Legacy clients may
	// keep providing them only on the final call, with delayed fetch resolution.
	StreamNext(id int64) (string, error)
	// StreamClose aborts the request and releases it. It must unblock a pending
	// StreamOpen or StreamNext. While StreamOpen is pending, id is the request
	// id from requestJSON; repeated calls and calls after the stream ends are safe.
	StreamClose(id int64)
}

// streamRequest is what StreamOpen receives. It matches the non-streaming
// request so a client can reuse its own transport code.
type streamRequest struct {
	ID         int64       `json:"id"`
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Headers    http.Header `json:"headers,omitempty"`
	BodyBase64 string      `json:"bodyBase64,omitempty"`
	Partition  string      `json:"partition,omitempty"`
}

// streamChunk is one StreamNext answer.
type streamChunk struct {
	Done        bool        `json:"done"`
	ChunkBase64 string      `json:"chunkBase64,omitempty"`
	Status      int         `json:"status,omitempty"`
	Headers     http.Header `json:"headers,omitempty"`
	FinalURL    string      `json:"finalUrl,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// streamBridge adapts HostStream to the chunk callback the plugin host expects.
type streamBridge struct {
	h HostStream
	// nextID assigns request ids, shared with the non-streaming path so a
	// cancellation never names another request's task.
	assign func() int64
}

// DoStream sends req and hands each piece of the body to onChunk.
//
// The request id comes from the same counter the non-streaming bridge uses, so
// Swift's task table cannot confuse a stream with an ordinary request.
func (s *streamBridge) DoStream(ctx context.Context, req *magpieplugin.Request, onChunk func([]byte) error) (*magpieplugin.Response, error) {
	return s.DoStreamWithHead(ctx, req, nil, onChunk)
}

// DoStreamWithHead exposes response metadata before body completion when the
// client supplies it. Older HostStream implementations retain their timing.
func (s *streamBridge) DoStreamWithHead(ctx context.Context, req *magpieplugin.Request, onHead func(*magpieplugin.Response) error, onChunk func([]byte) error) (*magpieplugin.Response, error) {
	var notify func(*streamAnswer) error
	if onHead != nil {
		notify = func(head *streamAnswer) error { return onHead(head.pluginResponse()) }
	}
	answer, err := s.sendWithHead(ctx, "magpie", req.Method, req.URL, req.Header, req.Body, notify, onChunk)
	if err != nil {
		return nil, err
	}
	return answer.pluginResponse(), nil
}

// streamAnswer is what a finished stream reported about the reply itself: the
// text arrived through onChunk, these are the head fields.
type streamAnswer struct {
	Status int
	Header http.Header
	URL    string
}

func (a *streamAnswer) pluginResponse() *magpieplugin.Response {
	return &magpieplugin.Response{Status: a.Status, StatusText: http.StatusText(a.Status), Header: a.Header, URL: a.URL}
}

// send is the part both callers share. The partition is a parameter because a
// plugin request and a standard AI request must not read each other's cookies.
func (s *streamBridge) send(ctx context.Context, partition, method, target string, header http.Header, body []byte, onChunk func([]byte) error) (*streamAnswer, error) {
	return s.sendWithHead(ctx, partition, method, target, header, body, nil, onChunk)
}

func (s *streamBridge) sendWithHead(ctx context.Context, partition, method, target string, header http.Header, body []byte, onHead func(*streamAnswer) error, onChunk func([]byte) error) (*streamAnswer, error) {
	if s == nil || s.h == nil {
		return nil, errors.New("mobile: this client cannot stream")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := s.assign()
	wire := streamRequest{
		ID: id, Method: method, URL: target,
		Headers: http.Header(header).Clone(), Partition: partition,
	}
	if len(body) > 0 {
		wire.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	// The request id is known before opening. Closing that id must also abort
	// a client waiting for response headers, before it can return a stream id.
	requestID := id
	openingClosed := make(chan struct{})
	stopOpening := context.AfterFunc(ctx, func() {
		defer close(openingClosed)
		s.h.StreamClose(requestID)
	})
	head, err := s.h.StreamOpen(string(encoded))
	if !stopOpening() {
		<-openingClosed
	}
	if err != nil {
		s.h.StreamClose(requestID)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	var opened struct {
		ID int64 `json:"id"`
		streamChunk
	}
	if head != "" {
		if err := json.Unmarshal([]byte(head), &opened); err != nil {
			s.h.StreamClose(id)
			return nil, fmt.Errorf("mobile: host stream reply: %w", err)
		}
	}
	// The client may number the stream itself; use its id when it did.
	if opened.ID != 0 {
		id = opened.ID
	}
	var closeOnce sync.Once
	closeStream := func() { closeOnce.Do(func() { s.h.StreamClose(id) }) }
	stopClose := context.AfterFunc(ctx, closeStream)
	defer func() {
		stopClose()
		closeStream()
	}()

	var answer *streamAnswer
	acceptHead := func(chunk streamChunk) error {
		if answer != nil || chunk.Status == 0 {
			return nil
		}
		answer = &streamAnswer{Status: chunk.Status, Header: chunk.Headers, URL: chunk.FinalURL}
		if onHead != nil {
			return onHead(answer)
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := acceptHead(opened.streamChunk); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := s.h.StreamNext(id)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return nil, fmt.Errorf("mobile: host stream chunk: %w", err)
		}
		if err := acceptHead(chunk); err != nil {
			return nil, err
		}
		if chunk.Error != "" {
			return nil, errors.New(chunk.Error)
		}
		if chunk.ChunkBase64 != "" {
			body, err := base64.StdEncoding.DecodeString(chunk.ChunkBase64)
			if err != nil {
				return nil, fmt.Errorf("mobile: host stream body: %w", err)
			}
			if err := onChunk(body); err != nil {
				return nil, err
			}
		}
		if chunk.Done {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if answer == nil {
				return nil, errors.New("mobile: host stream ended without response status")
			}
			return answer, nil
		}
	}
}

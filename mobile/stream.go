package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
// a chunk may be empty only when done is true.
type HostStream interface {
	// StreamOpen sends the request and returns {"id": n}. The client must
	// start reading the body without waiting for it to finish.
	StreamOpen(requestJSON string) (string, error)
	// StreamNext waits for the next piece and returns
	// {"done":bool,"chunkBase64":"…","status":n,"headers":{…},"finalUrl":"…"}.
	// status, headers and finalUrl are filled on the final call.
	StreamNext(id int64) (string, error)
	// StreamClose aborts the request and releases it. It is safe to call after
	// the stream ended.
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
	answer, err := s.send(ctx, "magpie", req.Method, req.URL, req.Header, req.Body, onChunk)
	if err != nil {
		return nil, err
	}
	return &magpieplugin.Response{
		Status: answer.Status, StatusText: http.StatusText(answer.Status),
		Header: answer.Header, URL: answer.URL,
	}, nil
}

// streamAnswer is what a finished stream reported about the reply itself: the
// text arrived through onChunk, these are the head fields.
type streamAnswer struct {
	Status int
	Header http.Header
	URL    string
}

// send is the part both callers share. The partition is a parameter because a
// plugin request and a standard AI request must not read each other's cookies.
func (s *streamBridge) send(ctx context.Context, partition, method, target string, header http.Header, body []byte, onChunk func([]byte) error) (*streamAnswer, error) {
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
	head, err := s.h.StreamOpen(string(encoded))
	if err != nil {
		return nil, err
	}
	var opened struct {
		ID int64 `json:"id"`
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
	defer s.h.StreamClose(id)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := s.h.StreamNext(id)
		if err != nil {
			return nil, err
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return nil, fmt.Errorf("mobile: host stream chunk: %w", err)
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
			return &streamAnswer{Status: chunk.Status, Header: chunk.Headers, URL: chunk.FinalURL}, nil
		}
	}
}

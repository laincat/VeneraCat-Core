package mobile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/laincat/veneracat-core/aiapi"
)

// Streaming a standard-protocol answer a piece at a time.
//
// The client owns the connection: it already has the transport, the cookies and
// the cancellation rules, and Core must not open a socket of its own for a
// phone. What the client does not have is the protocol's parsing, which lives
// in `aiapi` and is shared with every other caller. So the decoder is opened
// here, handed pieces as they arrive, and asked for the text each piece
// completed.
//
// This is a small registry rather than a callback interface because gomobile
// exports strings and numbers well and closures badly: the client holds a
// handle, pushes base64 pieces, and reads back a JSON array of deltas.

type aiStreams struct {
	mu      sync.Mutex
	next    atomic.Int64
	decoder map[int64]*aiStream
}

type aiStream struct {
	mu      sync.Mutex
	decoder *aiapi.StreamDecoder
}

var aiStreamRegistry = &aiStreams{decoder: map[int64]*aiStream{}}

func (s *aiStreams) lookup(handle int64) *aiStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decoder[handle]
}

func lockAIStream(handleText string) (*aiStream, error) {
	handle, err := parseHandle(handleText)
	if err != nil {
		return nil, err
	}
	stream := aiStreamRegistry.lookup(handle)
	if stream != nil {
		stream.mu.Lock()
		if stream.decoder != nil {
			return stream, nil
		}
		stream.mu.Unlock()
	}
	return nil, fmt.Errorf("mobile: stream decoder for handle %d is closed or unknown", handle)
}

// AIStreamOpen starts a decoder for one protocol and returns its handle as
// text.
//
// The handle is a string because gomobile maps `(int64, error)` to an
// out-parameter in Swift while `(string, error)` becomes a plain return with an
// NSError pointer, which is what the rest of this surface uses; one shape is
// easier to keep right than two.
func AIStreamOpen(name string) (_ string, err error) {
	defer guard(&err)
	decoder, err := aiapi.NewStreamDecoder(aiapi.Protocol(name))
	if err != nil {
		return "", err
	}
	handle := aiStreamRegistry.next.Add(1)
	aiStreamRegistry.mu.Lock()
	aiStreamRegistry.decoder[handle] = &aiStream{decoder: decoder}
	aiStreamRegistry.mu.Unlock()
	return strconv.FormatInt(handle, 10), nil
}

// AIStreamPush feeds one base64 piece of the body and returns the deltas it
// completed, as a JSON array of strings.
func AIStreamPush(handleText, chunkBase64 string) (_ string, err error) {
	defer guard(&err)
	stream, err := lockAIStream(handleText)
	if err != nil {
		return "", err
	}
	defer stream.mu.Unlock()
	raw, err := base64.StdEncoding.DecodeString(chunkBase64)
	if err != nil {
		return "", fmt.Errorf("mobile: the piece is not base64: %v", err)
	}
	deltas, err := stream.decoder.Push(raw)
	if err != nil {
		return "", err
	}
	if deltas == nil {
		deltas = []string{}
	}
	return encode(deltas)
}

// AIStreamDone reports whether the answer said it was complete, as "true" or
// "false".
func AIStreamDone(handleText string) (_ string, err error) {
	defer guard(&err)
	stream, err := lockAIStream(handleText)
	if err != nil {
		return "", err
	}
	defer stream.mu.Unlock()
	if stream.decoder.Done() {
		return "true", nil
	}
	return "false", nil
}

// AIStreamClose forgets a decoder. A caller that stopped early does this so the
// registry cannot grow with abandoned handles.
func AIStreamClose(handleText string) {
	handle, err := parseHandle(handleText)
	if err != nil {
		return
	}
	stream := aiStreamRegistry.lookup(handle)
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.decoder = nil
	aiStreamRegistry.mu.Lock()
	delete(aiStreamRegistry.decoder, handle)
	aiStreamRegistry.mu.Unlock()
}

func parseHandle(text string) (int64, error) {
	handle, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || handle <= 0 {
		return 0, fmt.Errorf("mobile: %q is not a stream handle", text)
	}
	return handle, nil
}

// unmarshalStrings reads the JSON array AIStreamPush returns. It exists so the
// test can decode the same wire form the client sees.
func unmarshalStrings(raw string, out *[]string) error {
	return json.Unmarshal([]byte(raw), out)
}

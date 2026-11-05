package mobile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	decoder map[int64]*aiapi.StreamDecoder
}

var aiStreamRegistry = &aiStreams{decoder: map[int64]*aiapi.StreamDecoder{}}

// AIStreamOpen starts a decoder for one protocol and returns its handle.
func AIStreamOpen(name string) (_ int64, err error) {
	defer guard(&err)
	decoder, err := aiapi.NewStreamDecoder(aiapi.Protocol(name))
	if err != nil {
		return 0, err
	}
	handle := aiStreamRegistry.next.Add(1)
	aiStreamRegistry.mu.Lock()
	aiStreamRegistry.decoder[handle] = decoder
	aiStreamRegistry.mu.Unlock()
	return handle, nil
}

// AIStreamPush feeds one base64 piece of the body and returns the deltas it
// completed, as a JSON array of strings.
func AIStreamPush(handle int64, chunkBase64 string) (_ string, err error) {
	defer guard(&err)
	aiStreamRegistry.mu.Lock()
	decoder := aiStreamRegistry.decoder[handle]
	aiStreamRegistry.mu.Unlock()
	if decoder == nil {
		return "", fmt.Errorf("mobile: no stream decoder for handle %d", handle)
	}
	raw, err := base64.StdEncoding.DecodeString(chunkBase64)
	if err != nil {
		return "", fmt.Errorf("mobile: the piece is not base64: %v", err)
	}
	deltas, err := decoder.Push(raw)
	if err != nil {
		return "", err
	}
	if deltas == nil {
		deltas = []string{}
	}
	return encode(deltas)
}

// AIStreamDone reports whether the answer said it was complete.
func AIStreamDone(handle int64) (bool, error) {
	aiStreamRegistry.mu.Lock()
	decoder := aiStreamRegistry.decoder[handle]
	aiStreamRegistry.mu.Unlock()
	if decoder == nil {
		return false, fmt.Errorf("mobile: no stream decoder for handle %d", handle)
	}
	return decoder.Done(), nil
}

// AIStreamClose forgets a decoder. A caller that stopped early does this so the
// registry cannot grow with abandoned handles.
func AIStreamClose(handle int64) {
	aiStreamRegistry.mu.Lock()
	delete(aiStreamRegistry.decoder, handle)
	aiStreamRegistry.mu.Unlock()
}

// unmarshalStrings reads the JSON array AIStreamPush returns. It exists so the
// test can decode the same wire form the client sees.
func unmarshalStrings(raw string, out *[]string) error {
	return json.Unmarshal([]byte(raw), out)
}

// Package sse decodes server-sent events from a client-owned stream.
// It opens no connections and does not interpret provider JSON or [DONE].
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const DefaultMaxEventBytes = 1 << 20

var ErrEventTooLarge = errors.New("sse: event exceeds byte limit")

// Event is one dispatched SSE message. ID is the last valid id seen on the
// stream, including an explicitly empty id. Data keeps JSON number text intact.
type Event struct {
	Type string
	Data string
	ID   string
}

// Decoder reads on demand. The caller owns cancellation and closing the reader.
// It is not safe for concurrent use. After a non-EOF error it is terminal.
type Decoder struct {
	scan  *bufio.Scanner
	limit int
	first bool
	id    string
	err   error
}

// NewDecoder limits each event block's wire bytes, including comments and field
// names (each line ending counts as one byte). Zero selects the 1 MiB default;
// explicit limits must be between 1 byte and 64 MiB. This also bounds lines and
// prevents a stream of comments from growing one unfinished block indefinitely.
func NewDecoder(r io.Reader, maxEventBytes int) (*Decoder, error) {
	if r == nil {
		return nil, errors.New("sse: nil reader")
	}
	if maxEventBytes == 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	if maxEventBytes < 1 || maxEventBytes > 64<<20 {
		return nil, errors.New("sse: byte limit must be between 1 and 67108864")
	}
	s := bufio.NewScanner(r)
	s.Split(splitLine)
	// Allow a line's terminator and an optional initial UTF-8 BOM in the
	// scanner; Next enforces the actual block limit separately.
	s.Buffer(make([]byte, min(4096, maxEventBytes+4)), maxEventBytes+4)
	return &Decoder{scan: s, limit: maxEventBytes, first: true}, nil
}

// Next returns a message only after a blank line terminates a block containing
// a data field. An unterminated final block is discarded, as SSE requires.
// Event-only and comment-only blocks are not messages. Unknown fields and retry
// instructions are ignored: reconnection is a transport policy of the client.
func (d *Decoder) Next() (Event, error) {
	if d.err != nil {
		return Event{}, d.err
	}
	var data strings.Builder
	kind := ""
	seenData, used := false, 0
	for d.scan.Scan() {
		line := d.scan.Text()
		if d.first {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			d.first = false
		}
		if line == "" {
			if seenData {
				if kind == "" {
					kind = "message"
				}
				return Event{Type: kind, Data: strings.TrimSuffix(data.String(), "\n"), ID: d.id}, nil
			}
			kind, used = "", 0
			continue
		}
		used += len(line) + 1
		if used > d.limit {
			d.err = ErrEventTooLarge
			return Event{}, d.err
		}
		// SSE is UTF-8; malformed input becomes replacement characters.
		line = strings.ToValidUTF8(line, "\ufffd")
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			seenData = true
			data.WriteString(value)
			data.WriteByte('\n')
		case "event":
			kind = value
		case "id":
			if !strings.ContainsRune(value, 0) {
				d.id = value
			}
		}
	}
	d.err = d.scan.Err()
	if errors.Is(d.err, bufio.ErrTooLong) {
		d.err = ErrEventTooLarge
	}
	if d.err == nil {
		d.err = io.EOF
	}
	return Event{}, d.err
}

// splitLine accepts LF, CRLF and CR, even when CRLF spans read chunks. At EOF
// the remaining text is a line, but not a blank-line dispatch on its own.
func splitLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			n := i + 1
			if n < len(data) && data[n] == '\n' {
				n++
			}
			return n, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

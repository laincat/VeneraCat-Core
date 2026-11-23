// Package sse decodes server-sent events from a client-owned stream.
// It opens no connections and does not interpret provider JSON or [DONE].
package sse

import (
	"bytes"
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
// It is not safe for concurrent use. Read and framing errors are terminal.
type Decoder struct {
	reader  io.Reader
	frame   Framer
	buffer  [4096]byte
	pending []byte
	err     error
}

// NewDecoder limits each event block, including comments and field names.
// LF, CR and CRLF count as one byte. Zero selects the 1 MiB default.
func NewDecoder(r io.Reader, maxEventBytes int) (*Decoder, error) {
	if r == nil {
		return nil, errors.New("sse: nil reader")
	}
	if maxEventBytes < 0 || maxEventBytes > 64<<20 {
		return nil, errors.New("sse: byte limit must be between 1 and 67108864")
	}
	return &Decoder{reader: r, frame: Framer{limit: maxEventBytes}}, nil
}

// Next returns the next blank-line-terminated event with at least one data field.
// An unfinished block at EOF is discarded.
func (d *Decoder) Next() (Event, error) {
	if d.frame.err != nil {
		return Event{}, d.frame.err
	}
	for emptyReads := 0; ; {
		if len(d.pending) != 0 {
			event, ready, rest := d.frame.next(d.pending)
			d.pending = rest
			if d.frame.err != nil {
				return Event{}, d.frame.err
			}
			if ready {
				return event, nil
			}
		}
		if d.err != nil {
			if len(d.frame.line) != 0 {
				d.frame.acceptLine(d.frame.line)
				d.frame.line = nil
			}
			if d.frame.err != nil {
				return Event{}, d.frame.err
			}
			return Event{}, d.err
		}
		n, err := d.reader.Read(d.buffer[:])
		d.pending, d.err = d.buffer[:n], err
		if n != 0 {
			emptyReads = 0
		} else if emptyReads++; emptyReads >= 100 && err == nil {
			d.err = io.ErrNoProgress
		}
	}
}

// Framer accepts arbitrary SSE byte chunks. Its zero value uses the default
// event limit. The caller serializes access to a Framer.
type Framer struct {
	limit    int
	used     int
	started  bool
	skipLF   bool
	line     []byte
	data     strings.Builder
	kind, id string
	err      error
}

// Push dispatches complete events in order. A framing error is terminal.
func (f *Framer) Push(chunk []byte, yield func(Event) error) error {
	for len(chunk) > 0 && f.err == nil {
		event, ready, rest := f.next(chunk)
		chunk = rest
		if ready {
			if err := yield(event); err != nil {
				return err
			}
		}
	}
	return f.err
}

func (f *Framer) byteLimit() int {
	if f.limit == 0 {
		return DefaultMaxEventBytes
	}
	return f.limit
}

func (f *Framer) next(chunk []byte) (Event, bool, []byte) {
	for len(chunk) != 0 && f.err == nil {
		if f.skipLF {
			f.skipLF = false
			if chunk[0] == '\n' {
				chunk = chunk[1:]
				continue
			}
		}
		end := bytes.IndexAny(chunk, "\r\n")
		partial := end < 0
		if partial {
			end = len(chunk)
		}
		limit := f.byteLimit()
		if !f.started {
			limit += 3
		}
		if end > limit-f.used-len(f.line) {
			f.err = ErrEventTooLarge
			break
		}
		if partial {
			f.line = append(f.line, chunk...)
			break
		}
		line := chunk[:end]
		if len(f.line) != 0 {
			line = append(f.line, line...)
			f.line = line[:0]
		}
		f.skipLF = chunk[end] == '\r'
		chunk = chunk[end+1:]
		if event, ready := f.acceptLine(line); ready {
			return event, true, chunk
		}
	}
	return Event{}, false, nil
}

func (f *Framer) acceptLine(line []byte) (Event, bool) {
	if !f.started {
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
		f.started = true
	}
	if len(line) == 0 {
		kind := f.kind
		f.kind, f.used = "", 0
		if f.data.Len() == 0 {
			return Event{}, false
		}
		if kind == "" {
			kind = "message"
		}
		event := Event{Type: kind, Data: strings.TrimSuffix(f.data.String(), "\n"), ID: f.id}
		f.data.Reset()
		return event, true
	}
	f.used += len(line) + 1
	if f.used > f.byteLimit() {
		f.err = ErrEventTooLarge
		return Event{}, false
	}
	field, value, _ := strings.Cut(strings.ToValidUTF8(string(line), "\ufffd"), ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "data":
		f.data.WriteString(value)
		f.data.WriteByte('\n')
	case "event":
		f.kind = value
	case "id":
		if !strings.ContainsRune(value, 0) {
			f.id = value
		}
	}
	return Event{}, false
}

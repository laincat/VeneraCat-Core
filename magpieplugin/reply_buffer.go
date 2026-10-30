package magpieplugin

import (
	"io"
	"sync"
)

// replyBuffer carries one reply's text from the package's push to the reader
// that turns it into events.
//
// It replaces a pipe because a pipe's write blocks until a reader takes it.
// The package pushes from inside its own hook, which is still running the
// JavaScript loop: if the reader had already stopped, the write would wait
// forever and the hook would never return. A buffer never blocks the writer;
// a reply that nobody reads is dropped when it is closed.
type replyBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	closed bool
	err    error
}

// newReplyBuffer returns an empty buffer.
func newReplyBuffer() *replyBuffer {
	b := &replyBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Write appends text and wakes the reader. It never waits for the reader, so
// a package can push as much as it likes without blocking its own hook.
func (b *replyBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	b.data = append(b.data, p...)
	b.cond.Broadcast()
	return len(p), nil
}

// Close ends the reply: the reader sees EOF once the buffered text is used up.
func (b *replyBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.cond.Broadcast()
}

// CloseWithError ends the reply with a reason the reader must report.
func (b *replyBuffer) CloseWithError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.err = err
	b.cond.Broadcast()
}

// Read returns the buffered text, waiting for more while the reply is open.
func (b *replyBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.data) == 0 {
		if b.closed {
			if b.err != nil {
				return 0, b.err
			}
			return 0, io.EOF
		}
		b.cond.Wait()
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

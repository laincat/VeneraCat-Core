package magpieplugin

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A push must never wait for the reader: the package pushes from inside its
// own hook, which is still holding the JavaScript loop.
func TestReplyBufferWriteDoesNotWaitForAReader(t *testing.T) {
	b := newReplyBuffer()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if _, err := b.Write([]byte("chunk")); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a write waited for a reader")
	}
}

func TestReplyBufferDeliversTheTextInOrder(t *testing.T) {
	b := newReplyBuffer()
	go func() {
		b.Write([]byte("one "))
		b.Write([]byte("two"))
		b.Close()
	}()
	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one two" {
		t.Fatalf("read %q", got)
	}
}

func TestReplyBufferCloseEndsTheRead(t *testing.T) {
	b := newReplyBuffer()
	b.Write([]byte("x"))
	b.Close()
	one := make([]byte, 1)
	if _, err := b.Read(one); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(one); err != io.EOF {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyBufferCloseWithErrorIsReported(t *testing.T) {
	b := newReplyBuffer()
	b.CloseWithError(io.ErrUnexpectedEOF)
	if _, err := b.Read(make([]byte, 1)); err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v", err)
	}
}

// A push after the reply ended is refused, not silently appended.
func TestReplyBufferRefusesAWriteAfterClose(t *testing.T) {
	b := newReplyBuffer()
	b.Close()
	if _, err := b.Write([]byte("late")); err == nil {
		t.Fatal("a write into a closed reply was accepted")
	}
}

// Many pushes from many goroutines all arrive, none of them lost or torn.
func TestReplyBufferKeepsEveryPush(t *testing.T) {
	b := newReplyBuffer()
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.Write([]byte("x"))
			}
		}()
	}
	go func() { wg.Wait(); b.Close() }()
	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != writers*each {
		t.Fatalf("read %d bytes, want %d", len(got), writers*each)
	}
	if strings.Count(string(got), "x") != writers*each {
		t.Fatal("the text was corrupted")
	}
}

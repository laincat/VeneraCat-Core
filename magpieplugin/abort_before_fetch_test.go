package magpieplugin

import (
	"context"
	"sync/atomic"
	"testing"
)

// Count entry into the client's transport, including calls that arrive with
// an already-cancelled context. A pre-aborted fetch must not start any of them.
type abortCountingHost struct{ calls atomic.Int32 }

func (h *abortCountingHost) Do(ctx context.Context, req *Request) (*Response, error) {
	h.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Response{Status: 200, Body: []byte("next request works"), URL: req.URL}, nil
}

type abortCountingStream struct{ *abortCountingHost }

func (h *abortCountingStream) DoStream(ctx context.Context, req *Request, chunk func([]byte) error) (*Response, error) {
	r, err := h.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return r, chunk(r.Body)
}

type abortCountingHeadStream struct{ *abortCountingStream }

func (h *abortCountingHeadStream) DoStreamWithHead(ctx context.Context, req *Request, head func(*Response) error, chunk func([]byte) error) (*Response, error) {
	r, err := h.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := head(r); err != nil {
		return nil, err
	}
	return r, chunk(r.Body)
}

func TestAlreadyAbortedFetchNeverStartsTransport(t *testing.T) {
	for _, mode := range []string{"whole", "legacy-stream", "head-stream"} {
		t.Run(mode, func(t *testing.T) {
			counter := &abortCountingHost{}
			var host Host = counter
			if mode == "legacy-stream" {
				host = &abortCountingStream{counter}
			} else if mode == "head-stream" {
				host = &abortCountingHeadStream{&abortCountingStream{counter}}
			}
			inst, seen := streamProbe(t, host, `
export async function probe() {
  const controller = new AbortController()
  controller.abort()
  try {
    await fetch("https://fixture.test/aborted", {signal: controller.signal})
    report("resolved")
  } catch (e) { report("rejected:" + e.message) }
}
export async function next() {
  const response = await fetch("https://fixture.test/next")
  return await response.text()
}`)
			if _, err := inst.Call("probe", nil); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-seen:
				if got != "rejected:context canceled" {
					t.Errorf("pre-aborted fetch = %q", got)
				}
			default:
				t.Error("pre-aborted fetch produced no outcome")
			}
			if n := counter.calls.Load(); n != 0 {
				t.Errorf("pre-aborted fetch started %d transport calls", n)
			}
			before := counter.calls.Load()
			got, err := inst.Call("next", nil)
			if err != nil || got != "next request works" {
				t.Fatalf("subsequent fetch = %v, %v", got, err)
			}
			if counter.calls.Load() != before+1 {
				t.Fatal("subsequent fetch did not reach the transport exactly once")
			}
		})
	}
}

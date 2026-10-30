package mobile

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuditCancelBeforeCallStarts(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	id := c.NewCall()
	c.Cancel(id)
	ctx, done := c.ctx(id)
	defer done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation was lost before call registration")
	}
}

func TestAuditCloseCancelsActiveAndFutureCalls(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	ctx, done := c.ctx(c.NewCall())
	defer done()
	anonymous, finishAnonymous := c.ctx(0)
	defer finishAnonymous()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close left an active call running")
	}
	if !errors.Is(anonymous.Err(), context.Canceled) {
		t.Fatal("Close left an anonymous call running")
	}
	future, finish := c.ctx(c.NewCall())
	defer finish()
	if !errors.Is(future.Err(), context.Canceled) {
		t.Fatal("closed Core accepted a new call")
	}
}

package mobile

import "testing"

// Swift reserves a call id before it queues the blocking call, so a method that
// returns before it starts (an unknown source, a bad argument) must release the
// reservation. Otherwise every failed call leaks one entry in each map for the
// life of the session.
func TestAuditEarlyReturnReleasesTheReservation(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	for i := 0; i < 3; i++ {
		id := c.NewCall()
		if _, err := c.CallComic(id, "ghost", "search.load", "[]"); err == nil {
			t.Fatal("an unknown source was accepted")
		}
		if _, err := c.VideoSearch(id, "ghost", "q", ""); err == nil {
			t.Fatal("an unknown video source was accepted")
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cancels) != 0 || len(c.reserved) != 0 {
		t.Fatalf("cancels=%d reserved=%d after failing calls", len(c.cancels), len(c.reserved))
	}
}

// A successful call releases its reservation too, and a second Cancel of the
// same id is still harmless.
func TestAuditFinishedCallReleasesTheReservation(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}})
	id := c.NewCall()
	if _, err := c.Install(id, "comic", comic, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	c.Cancel(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cancels) != 0 || len(c.reserved) != 0 {
		t.Fatalf("cancels=%d reserved=%d after a finished call", len(c.cancels), len(c.reserved))
	}
}

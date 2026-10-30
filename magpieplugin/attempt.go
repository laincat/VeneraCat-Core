package magpieplugin

import "errors"

// ErrNoRetryAfterOutput says an attempt may not be handed to another vendor:
// the reader has already been shown part of the reply, and trying again would
// either repeat it or contradict it.
var ErrNoRetryAfterOutput = errors.New("magpieplugin: output has started, so this attempt cannot be retried")

// Attempt tracks one try at a reply. A client uses it to decide whether a
// failure may fall back to another account or provider.
type Attempt struct {
	// OutputStarted is set once any non-empty text or reasoning reached the
	// reader.
	OutputStarted bool
}

// MayRetry says whether another attempt is allowed.
func (a *Attempt) MayRetry() error {
	if a != nil && a.OutputStarted {
		return ErrNoRetryAfterOutput
	}
	return nil
}

// Saw records an event's effect on the attempt. A done event, an empty delta or
// a vendor error does not count as output: none of them showed the reader text.
func (a *Attempt) Saw(event Event) {
	if a == nil {
		return
	}
	switch event.Kind {
	case EventText, EventReasoning:
		if event.Text != "" {
			a.OutputStarted = true
		}
	}
}

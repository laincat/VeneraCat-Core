package magpieplugin

// Pin says how far a retry may move.
//
// Choosing who answers and deciding whether a retry may pick someone else are two
// different decisions, and conflating them is how a conversation planned around
// one model quietly gets answered by another: the plan said "this is a hard
// question, use the strong model", the first account was busy, and the reply
// arrived from a small one.
//
// A pin does not stop retries. It says which part of the route is allowed to
// change: the credential (an exhausted key), or nothing at all.
type Pin string

const (
	// PinAny lets a retry use any endpoint that is left. This is the default
	// because most callers pass a list that is already one route.
	PinAny Pin = ""
	// PinAccount keeps the endpoint's account and lets the address change.
	PinAccount Pin = "account"
	// PinModel keeps the model and lets the account change: a busy or rejected key
	// is exactly what a second account is for, and the model is what the reply was
	// planned around.
	PinModel Pin = "model"
)

// allows reports whether a later endpoint may stand in for the first one under
// this pin.
//
// An empty field means the caller did not say, and a pin cannot be enforced on a
// label nobody wrote: the endpoint is kept rather than silently dropped, because
// dropping it would turn an unknown into a refusal to try.
func (p Pin) allows(first, next Endpoint) bool {
	switch p {
	case PinAccount:
		return first.Account == "" || next.Account == "" || first.Account == next.Account
	case PinModel:
		return first.Model == "" || next.Model == "" || first.Model == next.Model
	default:
		return true
	}
}

// reason is what a caller is told when the walk ended because the pin left
// nowhere to go.
func (p Pin) reason() string {
	return string(p)
}

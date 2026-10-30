package js

import "fmt"

// DuplicateKeyError reports a validated key already held by this runtime.
// It is produced by the loader, never inferred from a script's exception text.
// Kind identifies the incoming script so hosts can still enforce type checks.
type DuplicateKeyError struct {
	Key  string
	Kind Kind
}

func (e *DuplicateKeyError) Error() string { return e.Unwrap().Error() }

// Unwrap preserves the existing LoadError classification and message.
func (e *DuplicateKeyError) Unwrap() error {
	return &LoadError{Reason: fmt.Sprintf("key(%s) already exists", e.Key)}
}

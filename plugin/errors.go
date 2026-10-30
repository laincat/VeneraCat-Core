package plugin

import "fmt"

// KeyExistsError identifies a registry collision without parsing error text.
// Callers may offer an explicit same-type replacement; this error itself does
// not install or update anything. errors.Is still matches ErrExists.
type KeyExistsError struct {
	Key string
}

func (e *KeyExistsError) Error() string { return fmt.Sprintf("%s: %s", ErrExists, e.Key) }
func (e *KeyExistsError) Unwrap() error { return ErrExists }

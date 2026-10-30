package js

import (
	"context"
	"encoding/json"
)

// evalIn runs src inside the engine of an installed source (tests only).
func (r *Runtime) evalIn(ctx context.Context, key, src string) (json.RawMessage, error) {
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	return evalOn(ctx, e, src)
}

//go:build !windows && !darwin

package webcookie

import (
	"context"
	"net/http"
	"net/url"
)

func read(context.Context, *url.URL, Options) ([]*http.Cookie, error) { return nil, ErrUnsupported }

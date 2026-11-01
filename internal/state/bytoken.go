package state

import (
	"context"
	"net/http"
)

type byTokenKey struct{}

// WithToken marks r as let in by the machine's token (TokenHeader), not a
// cookie: only stui's own programs on this machine hold it (stui and its
// hooks), so it is what says "from this machine". The
// agent's gate sets it, after it checked the token.
func WithToken(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), byTokenKey{}, true))
}

// ByToken is whether the gate let r in by the machine's token. Where a
// request comes from is not that: a page in a browser on this machine
// connects from loopback too, and so does a local proxy.
func ByToken(r *http.Request) bool {
	v, _ := r.Context().Value(byTokenKey{}).(bool)
	return v
}

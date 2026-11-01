package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sophonz-labs/stui/internal/state"
)

func TestTokenRequired(t *testing.T) {
	s := &server{token: "secret"}
	for _, tok := range []string{"", "wrong", "secre"} {
		r := httptest.NewRequest("GET", "/api/version", nil)
		if tok != "" {
			r.Header.Set(state.TokenHeader, tok)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d, want 401", tok, w.Code)
		}
	}
}

func TestAgentName(t *testing.T) {
	for name, ok := range map[string]bool{"claude": true, "codex": true, "pi-1.4": true, "a b": false, "x;rm": false, "": false} {
		if agentName.MatchString(name) != ok {
			t.Errorf("%q: %v", name, !ok)
		}
	}
}

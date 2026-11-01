package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

type ServiceOptions struct {
	Env Env
	// nil lets every request the server's gate let in. A read-only viewer
	// is not the owner and gets 403: the limits are the owner's account.
	IsOwner func(*http.Request) bool
	// Told of each crossing of a level, once per window and reset: the
	// usage-high push. nil sends none.
	Crossed func(Crossing)
}

type Service struct {
	o ServiceOptions

	mu   sync.Mutex
	last Report
	at   time.Time
}

func NewService(o ServiceOptions) *Service { return &Service{o: o} }

// Report is Read, kept for two seconds: a phone that opens three screens
// at once reads the files once.
func (s *Service) Report() Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.o.Env.now()
	if !s.at.IsZero() && now.Sub(s.at) < 2*time.Second {
		return s.last
	}
	s.last, s.at = Read(s.o.Env), now
	return s.last
}

// ServeHTTP answers GET /api/usage and PUT /api/usage/guard, and is false
// for any other path.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/usage" && r.URL.Path != "/api/usage/guard" {
		return false
	}
	if s.o.IsOwner != nil && !s.o.IsOwner(r) {
		writeJSON(w, 403, map[string]any{"error": "read-only"})
		return true
	}
	if r.URL.Path == "/api/usage/guard" {
		s.serveGuard(w, r)
		return true
	}
	if r.Method != "GET" {
		w.Header().Set("allow", "GET")
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	rep := s.Report()
	writeJSON(w, 200, struct {
		Report
		Guard GuardReport `json:"guard"`
	}{rep, GuardStatus(ReadGuard(s.o.Env), rep, s.o.Env.now())})
	return true
}

// serveGuard is GET /api/usage/guard (the guard alone) and PUT with
// {"on": true|false}: stui usage guard on|off from an app.
func (s *Service) serveGuard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
	case "PUT":
		var body struct {
			On *bool `json:"on"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil || body.On == nil {
			writeJSON(w, 400, map[string]any{"error": `want {"on": true|false}`})
			return
		}
		if err := SetGuard(s.o.Env, *body.On); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
	default:
		w.Header().Set("allow", "GET, PUT")
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, 200, GuardStatus(ReadGuard(s.o.Env), s.Report(), s.o.Env.now()))
}

// Run looks for crossings every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if s.o.Crossed == nil {
		return
	}
	alerts := NewAlerts(s.o.Env)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for _, c := range alerts.Check(Read(s.o.Env), s.o.Env.now()) {
			s.o.Crossed(c)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

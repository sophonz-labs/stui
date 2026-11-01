package watch

import (
	"encoding/base64"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OSC 7501, the Program Status Protocol: a program in a pane says what it is
// doing, ESC ] 7501 ; state=working:app=cargo:progress=40 ESC \. The rules
// follow the specification as Ghostty reads it (herdr's terminal):
// colon-separated key=value pairs; a malformed pair or an unknown key is
// skipped; the last value of a key wins; and the whole report is dropped
// when state is missing or unknown, when any key or value breaks its
// limit, when an id is malformed, or when title or msg is not base64 of
// printable UTF-8. stui keeps only the root record (a report without id):
// that is the pane's program speaking for itself.
//
// stui does not answer the "?" query. tmux answers the device-attributes
// query a program sends after it at once, so an answer from stui, which
// arrives over control mode a moment later, is always too late (Claude Code
// 2.1.314 logs "programStatus=no (probe: no reply to OSC 7501 ; ?)" either
// way). A program that reports without asking first is heard.

// Report is what a pane's program last said about itself.
type Report struct {
	// idle, working, done, blocked or error, the program's own word.
	State string `json:"state"`
	// permission, question or auth: what a blocked program needs.
	Kind string `json:"kind,omitempty"`
	// 0..100 while working or blocked, when the program said.
	Progress *int   `json:"progress,omitempty"`
	App      string `json:"app,omitempty"`
	Title    string `json:"title,omitempty"`
	Msg      string `json:"msg,omitempty"`
	// When it said it (ms).
	At int64 `json:"at"`
}

// statusReport is one parsed 7501 report.
type statusReport struct {
	Report
	id string
}

const (
	statusMaxBody     = 4096 - len("\x1b]7501;") - len("\x1b\\")
	statusMaxKey      = 16
	statusMaxApp      = 32
	statusMaxTitleEnc = 256
	statusMaxTitle    = 192
	statusMaxMsgEnc   = 2732
	statusMaxMsg      = 2048
	statusMaxID       = 128
	statusMaxIDPart   = 32
	statusMaxIDDepth  = 8
)

var statusStates = map[string]bool{"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true}
var statusKinds = map[string]bool{"permission": true, "question": true, "auth": true}

// stateOf is the pane state a reported word stands for.
func stateOf(word string) string {
	switch word {
	case "working":
		return Working
	case "done":
		return Done
	case "blocked":
		return Waiting
	case "error":
		return Error
	}
	return Idle
}

// parseStatus reads the body of an OSC 7501 (what follows "7501;"). ok is
// false for the "?" query and for a report the rules drop.
func parseStatus(body string) (statusReport, bool) {
	var r statusReport
	if len(body) > statusMaxBody || body == "?" {
		return r, false
	}
	type pair struct{ k, v string }
	var pairs []pair
	for _, p := range strings.Split(body, ":") {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(p[:eq])
		if len(k) > statusMaxKey {
			return r, false
		}
		pairs = append(pairs, pair{k, strings.TrimSpace(p[eq+1:])})
	}
	// Limits hold for every value, even one a later pair replaces.
	for _, p := range pairs {
		switch p.k {
		case "id":
			if !validID(p.v) {
				return r, false
			}
		case "app":
			if len(p.v) > statusMaxApp {
				return r, false
			}
		case "title":
			if len(p.v) > statusMaxTitleEnc {
				return r, false
			}
		case "msg":
			if len(p.v) > statusMaxMsgEnc {
				return r, false
			}
		}
	}
	last := func(k string) (string, bool) {
		v, ok := "", false
		for _, p := range pairs {
			if p.k == k && isValue(p.v) {
				v, ok = p.v, true
			}
		}
		return v, ok
	}
	state, _ := last("state")
	if !statusStates[state] {
		return r, false
	}
	r.State = state
	r.id, _ = last("id")
	if v, ok := last("app"); ok && isName(v) {
		r.App = v
	}
	if v, ok := last("kind"); ok && state == "blocked" && statusKinds[v] {
		r.Kind = v
	}
	if v, ok := last("progress"); ok && (state == "working" || state == "blocked") {
		if n, ok := percent(v); ok {
			r.Progress = &n
		}
	}
	for _, t := range []struct {
		key string
		max int
		to  *string
	}{{"title", statusMaxTitle, &r.Title}, {"msg", statusMaxMsg, &r.Msg}} {
		v, ok := last(t.key)
		if !ok || v == "" {
			continue
		}
		text, ok := decodeText(v)
		if !ok || len(text) > t.max {
			return r, false
		}
		*t.to = text
	}
	return r, true
}

const nameBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.+-"

func isName(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !strings.ContainsRune(nameBytes, r) })
}

func isValue(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !strings.ContainsRune(nameBytes+",/=", r) })
}

// validID is names joined by single slashes, within the limits.
func validID(v string) bool {
	if len(v) > statusMaxID {
		return false
	}
	parts := strings.Split(v, "/")
	if len(parts) > statusMaxIDDepth {
		return false
	}
	for _, p := range parts {
		if len(p) > statusMaxIDPart || !isName(p) {
			return false
		}
	}
	return true
}

// percent is a whole number from 0 to 100.
func percent(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		if n = n*10 + int(c-'0'); n > 100 {
			return 0, false
		}
	}
	return n, true
}

// decodeText is base64 (padding optional) of UTF-8 with no control
// characters.
func decodeText(v string) (string, bool) {
	enc := base64.StdEncoding
	if !strings.HasSuffix(v, "=") {
		enc = base64.RawStdEncoding
	}
	b, err := enc.DecodeString(v)
	if err != nil || !utf8.Valid(b) {
		return "", false
	}
	s := string(b)
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", false
	}
	return s, true
}

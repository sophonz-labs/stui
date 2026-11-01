package chat

import (
	"strings"
	"unicode/utf8"
)

// LastAsked is the first line of the last prompt a person or another agent
// gave pane's agent, at most 80 characters; "" when its conversation cannot
// be read or has none. stui's own messages ("[stui] ...") and Claude's
// background-task notices are not prompts. For whoever adopts that agent.
func (s *Service) LastAsked(pane string) string {
	at := ref{pane: pane}
	snap, _, err := s.window(at, s.resolveRef(at), -1, 0, 80)
	if err != nil {
		return ""
	}
	for i := len(snap.Messages) - 1; i >= 0; i-- {
		m := snap.Messages[i]
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type != "text" {
				continue
			}
			t := strings.TrimSpace(b.Text)
			if t == "" || strings.HasPrefix(t, "[stui]") || strings.HasPrefix(t, "<task-notification>") {
				continue
			}
			line, _, _ := strings.Cut(t, "\n")
			line = strings.TrimSpace(line)
			if utf8.RuneCountInString(line) > 80 {
				line = string([]rune(line)[:79]) + "…"
			}
			return line
		}
	}
	return ""
}

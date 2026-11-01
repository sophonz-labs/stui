package chat

import "strings"

// TurnEnd is how the last turn of a pane's agent ended, as its
// conversation tells it: for the reason a worker stopped (stopReason).
type TurnEnd struct {
	// Known: the conversation could be read.
	Known bool
	// Answer is the assistant's text after the last prompt.
	Answer string
	// Error is the text of the turn's last reply when the agent wrote it
	// down as its own error (Claude's "API Error: ...", its usage limit).
	Error string
	// Interrupted: the turn was stopped before it finished (Esc).
	Interrupted bool
}

// LastTurn is how pane's agent's last turn ended. A conversation that
// cannot be read gives the zero TurnEnd.
func (s *Service) LastTurn(pane string) TurnEnd {
	at := ref{pane: pane}
	snap, _, err := s.window(at, s.resolveRef(at), -1, 0, 40)
	if err != nil {
		return TurnEnd{}
	}
	return lastTurn(snap.Messages)
}

func lastTurn(ms []Message) TurnEnd {
	te := TurnEnd{Known: true}
	var parts []string
	last := true
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.Role == RoleUser {
			break
		}
		if m.Role == RoleSystem {
			for _, b := range m.Blocks {
				if b.Type == BlockInterrupted {
					te.Interrupted = true
				}
			}
			continue
		}
		if m.Role != RoleAssistant {
			continue
		}
		var t []string
		for _, b := range m.Blocks {
			if b.Type == BlockText && strings.TrimSpace(b.Text) != "" {
				t = append(t, b.Text)
			}
		}
		if len(t) == 0 {
			continue
		}
		text := strings.Join(t, "\n")
		if last && m.Error {
			te.Error = text
		}
		last = false
		parts = append([]string{text}, parts...)
	}
	te.Answer = strings.Join(parts, "\n\n")
	return te
}

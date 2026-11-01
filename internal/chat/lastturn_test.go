package chat

import "testing"

// How the last turn ended: the reply after the last prompt, the agent's
// own error, an interrupt.
func TestLastTurn(t *testing.T) {
	text := func(role, s string) Message { return Message{Role: role, Blocks: []Block{{Type: BlockText, Text: s}}} }
	limit := "You've hit your weekly limit · resets 3pm (Asia/Seoul)"
	errMsg := text(RoleAssistant, limit)
	errMsg.Error = true

	te := lastTurn([]Message{text(RoleUser, "go"), text(RoleAssistant, "on it"), {Role: RoleTool}, errMsg})
	if te.Error != limit || te.Answer != "on it\n\n"+limit || te.Interrupted || !te.Known {
		t.Fatalf("limit: %+v", te)
	}
	// An error from an earlier turn is not this one's.
	te = lastTurn([]Message{text(RoleUser, "a"), errMsg, text(RoleUser, "b"), text(RoleAssistant, "done")})
	if te.Error != "" || te.Answer != "done" {
		t.Fatalf("earlier error: %+v", te)
	}
	// Nor is an error followed by a reply.
	te = lastTurn([]Message{text(RoleUser, "a"), errMsg, text(RoleAssistant, "retried, done")})
	if te.Error != "" {
		t.Fatalf("error then a reply: %+v", te)
	}
	te = lastTurn([]Message{text(RoleUser, "a"), text(RoleAssistant, "half"), {Role: RoleSystem, Blocks: []Block{{Type: BlockInterrupted}}}})
	if !te.Interrupted || te.Answer != "half" {
		t.Fatalf("interrupted: %+v", te)
	}
	if te := lastTurn(nil); te.Answer != "" || te.Interrupted || !te.Known {
		t.Fatalf("empty: %+v", te)
	}
}

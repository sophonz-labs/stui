package mux

import "testing"

// Counting the panes of a session that are running an agent. Agent teams put
// each teammate in its own pane, so this is what tells a session with one
// agent in it apart from a session running five.
func TestAgentSessionCounts(t *testing.T) {
	panes := map[string][]string{
		"team":  {"%1", "%2", "%3", "%4"},
		"plain": {"%9"},
		"empty": nil,
	}
	agents := map[string]string{"%1": "claude", "%2": "claude", "%3": "codex"}
	agentOf := func(p string) string { return agents[p] }

	list := []SessionInfo{{Name: "team"}, {Name: "plain"}, {Name: "empty"}, {Name: "gone"}}
	AgentSessionCounts(list, panes, agentOf)
	for i, want := range []int{3, 0, 0, 0} {
		if list[i].Agents != want {
			t.Errorf("%s: %d agents, want %d", list[i].Name, list[i].Agents, want)
		}
	}

	// Without either half the count is left alone: unknown is not the same
	// as none, and the column stays blank rather than claiming zero.
	none := []SessionInfo{{Name: "team", Agents: 7}}
	AgentSessionCounts(none, nil, agentOf)
	AgentSessionCounts(none, panes, nil)
	if none[0].Agents != 7 {
		t.Errorf("overwrote a count with no way to know: %d", none[0].Agents)
	}
}

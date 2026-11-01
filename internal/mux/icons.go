package mux

import "github.com/sophonz-labs/stui/internal/cmdicon"

// resolve is the icon for a pane running command on tty. Its argv is read
// only when the command is a wrapper (npx, node, python, sudo...), and only
// ever reaches cmdicon.
func resolve(command, tty string) cmdicon.Match {
	var argv func() []string
	if tty != "" && cmdicon.IsWrapper(command) {
		argv = func() []string { return cmdicon.ForegroundArgs(tty) }
	}
	return cmdicon.Resolve("", command, argv)
}

// AgentIcons corrects windows' icons with what agent watch knows: agentOf
// gives a pane's agent kind (claude, codex, gemini) or "".
func AgentIcons(list []WindowInfo, agentOf func(pane string) string) {
	if agentOf == nil {
		return
	}
	for i := range list {
		if a := agentOf(list[i].Pane); a != "" && list[i].Pane != "" {
			m := cmdicon.Resolve(a, list[i].proc, nil)
			list[i].Icon, list[i].Command = m.Icon, m.Command
		}
	}
}

// AgentSessionIcons is AgentIcons for sessions.
// AgentSessionCounts fills in how many panes of each session are running an
// agent. panes is the whole server's panes by session; agentOf says whether
// one has an agent. Either missing leaves every count at zero, which reads
// as "not known" rather than "none" -- the column is simply blank then.
func AgentSessionCounts(list []SessionInfo, panes map[string][]string, agentOf func(pane string) string) {
	if agentOf == nil || panes == nil {
		return
	}
	for i := range list {
		n := 0
		for _, pane := range panes[list[i].Name] {
			if agentOf(pane) != "" {
				n++
			}
		}
		list[i].Agents = n
	}
}

func AgentSessionIcons(list []SessionInfo, agentOf func(pane string) string) {
	if agentOf == nil {
		return
	}
	for i := range list {
		if a := agentOf(list[i].Pane); a != "" && list[i].Pane != "" {
			m := cmdicon.Resolve(a, list[i].proc, nil)
			list[i].Icon, list[i].Command = m.Icon, m.Command
		}
	}
}

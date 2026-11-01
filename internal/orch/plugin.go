package orch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/sophonz-labs/stui/internal/home"
)

// The Claude Code plugin `stui`: two skill stubs that
// send the agent to `stui skills get`, so the guide always matches the stui that
// runs the commands. Kept in ~/.stui/plugins as a local marketplace.

func PluginRoot() string { return filepath.Join(home.Dir(), "plugins") }

const orchestrateSkill = `---
name: orchestrate
description: "Use when the user asks to split work across several coding agents, run agents in parallel, delegate a task to another agent and supervise it, or coordinate workers in tmux through stui. Not for a single quick subagent."
allowed-tools: "Bash(stui *)"
---

Coordinate other coding agents (Claude Code, Codex) through stui: each worker
runs in its own tmux window, and the user can watch the delegation graph.

Before running any stui orchestration command, load the guide that matches the
installed stui:

` + "```bash\nst skills get orchestrate\n```" + `

Follow it exactly: stui orch start, then stui worker start for each worker, then
stui worker wait, stui worker read, and report.
`

const workerSkill = `---
name: worker
description: "Use when the environment variable STUI_RUN is set: you were started as a worker by stui orchestration and must report with stui worker done."
user-invocable: false
allowed-tools: "Bash(stui *)"
---

You are a worker in an stui orchestration run. Your task is in the file named by
$STUI_TASK_FILE. Load your obligations first:

` + "```bash\nst skills get worker\n```" + `
`

// WritePlugin writes the marketplace and the plugin for stui version v and
// returns the marketplace directory.
func WritePlugin(version string) (string, error) {
	root := PluginRoot()
	files := map[string]string{
		".claude-plugin/marketplace.json": mustJSON(map[string]any{
			"name":     "stui",
			"owner":    map[string]string{"name": "sophonz-labs"},
			"metadata": map[string]string{"description": "Orchestration for stui: coding agents handing work to other coding agents in tmux"},
			"plugins":  []map[string]string{{"name": "stui", "source": "./stui", "description": "Delegate work to other coding agents in tmux through stui"}},
		}),
		"stui/.claude-plugin/plugin.json": mustJSON(map[string]any{
			"name":        "stui",
			"version":     strings.TrimPrefix(version, "v"),
			"description": "Delegate work to other coding agents in tmux through stui, and watch it as a graph",
			"author":      map[string]string{"name": "sophonz-labs"},
		}),
		"stui/skills/orchestrate/SKILL.md": orchestrateSkill,
		"stui/skills/worker/SKILL.md":      workerSkill,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

// CodexSkillsDir is where Codex looks for skills ($CODEX_HOME/skills).
func CodexSkillsDir() string {
	h := os.Getenv("CODEX_HOME")
	if h == "" {
		d, _ := os.UserHomeDir()
		h = filepath.Join(d, ".codex")
	}
	return filepath.Join(h, "skills")
}

// WriteCodexSkills puts the same two stubs where Codex reads skills.
func WriteCodexSkills() (string, error) {
	d := CodexSkillsDir()
	for name, body := range map[string]string{"stui-orchestrate": orchestrateSkill, "stui-worker": workerSkill} {
		p := filepath.Join(d, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(strings.Replace(body, "name: "+strings.TrimPrefix(name, "stui-"), "name: "+name, 1)), 0o644); err != nil {
			return "", err
		}
	}
	return d, nil
}

func RemoveCodexSkills() {
	d := CodexSkillsDir()
	os.RemoveAll(filepath.Join(d, "stui-orchestrate"))
	os.RemoveAll(filepath.Join(d, "stui-worker"))
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n"
}

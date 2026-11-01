package hookclient

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/proc"
)

// A worker that owns some paths is held
// to them here: Claude Code's PreToolUse for a file edit outside them is
// answered with a denial and the reason, which Claude reads and follows.
// The owned paths are on the worker's pane (@stui-owns, @stui-root), read from
// tmux. Anything that fails -- no tmux, no options, a hook input stui does not
// know -- lets the edit through: stui never stops an agent by accident.

// editTools are Claude Code's tools that change a file, with the field
// naming it.
var editTools = map[string]string{
	"Edit": "file_path", "MultiEdit": "file_path", "Write": "file_path", "NotebookEdit": "notebook_path",
}

// ownsCheck is the denial to print for a Claude PreToolUse, or nil.
func ownsCheck(kind, event string, raw []byte, owns func() ([]string, string)) []byte {
	if kind != "claude" || event != "PreToolUse" {
		return nil
	}
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
		Cwd       string         `json:"cwd"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	field, ok := editTools[in.ToolName]
	if !ok {
		return nil
	}
	path, _ := in.ToolInput[field].(string)
	if path == "" {
		return nil
	}
	patterns, root := owns()
	if len(patterns) == 0 {
		return nil
	}
	if root == "" {
		root = in.Cwd
	}
	if !strings.HasPrefix(path, "/") && in.Cwd != "" {
		path = in.Cwd + "/" + path
	}
	allowed, why := orch.MayEdit(patterns, root, path)
	if allowed {
		return nil
	}
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": why,
		},
	})
	return b
}

// paneOwns reads a pane's owned paths from its tmux server ($TMUX).
func paneOwns(pane string) func() ([]string, string) {
	return func() ([]string, string) {
		v := paneOptions(pane, orch.OptOwns, orch.OptRoot)
		if v == nil {
			return nil, ""
		}
		return orch.ParseOwns(v[0]), v[1]
	}
}

// paneOptions are the pane's options of those names, read from its tmux
// server ($TMUX) in one call; nil when they cannot be read.
func paneOptions(pane string, names ...string) []string {
	socket, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	if socket == "" || pane == "" {
		return nil
	}
	bin := "tmux"
	if p, err := exec.LookPath("tmux"); err == nil {
		bin = p
	} else if p := os.Getenv("STUI_TMUX_BIN"); p != "" {
		bin = p
	}
	f := make([]string, len(names))
	for i, n := range names {
		f[i] = "#{" + n + "}"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-S", socket, "display-message", "-p", "-t", pane, strings.Join(f, "\t"))
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	v := strings.Split(strings.TrimRight(string(out), "\n"), "\t")
	if len(v) != len(names) {
		return nil
	}
	return v
}

// writeDecision prints a hook's answer for the agent.
func writeDecision(w io.Writer, b []byte) {
	if len(b) > 0 {
		w.Write(append(b, '\n'))
	}
}

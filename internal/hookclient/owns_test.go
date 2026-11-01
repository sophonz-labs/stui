package hookclient

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOwnsCheck(t *testing.T) {
	owns := func() ([]string, string) { return []string{"src/auth/**"}, "/work/app" }
	input := func(tool, field, path string) []byte {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "cwd": "/work/app", "tool_input": map[string]any{field: path}})
		return b
	}
	deny := ownsCheck("claude", "PreToolUse", input("Edit", "file_path", "/work/app/src/billing/pay.go"), owns)
	var raw map[string]map[string]string
	if json.Unmarshal(deny, &raw) != nil {
		t.Fatalf("not JSON: %s", deny)
	}
	h := raw["hookSpecificOutput"]
	if h["hookEventName"] != "PreToolUse" || h["permissionDecision"] != "deny" || !strings.Contains(h["permissionDecisionReason"], "src/auth/**") {
		t.Fatalf("%s", deny)
	}
	for name, got := range map[string][]byte{
		"inside":          ownsCheck("claude", "PreToolUse", input("Write", "file_path", "/work/app/src/auth/x.go"), owns),
		"relative inside": ownsCheck("claude", "PreToolUse", input("Edit", "file_path", "src/auth/x.go"), owns),
		"outside folder":  ownsCheck("claude", "PreToolUse", input("Edit", "file_path", "/tmp/notes.md"), owns),
		"not an edit":     ownsCheck("claude", "PreToolUse", input("Read", "file_path", "/work/app/src/billing/pay.go"), owns),
		"another event":   ownsCheck("claude", "PostToolUse", input("Edit", "file_path", "/work/app/src/billing/pay.go"), owns),
		"codex":           ownsCheck("codex", "PreToolUse", input("Edit", "file_path", "/work/app/src/billing/pay.go"), owns),
		"owns nothing":    ownsCheck("claude", "PreToolUse", input("Edit", "file_path", "/work/app/src/billing/pay.go"), func() ([]string, string) { return nil, "" }),
		"bad input":       ownsCheck("claude", "PreToolUse", []byte("{"), owns),
	} {
		if got != nil {
			t.Errorf("%s: denied %s", name, got)
		}
	}
	if ownsCheck("claude", "PreToolUse", input("NotebookEdit", "notebook_path", "/work/app/nb.ipynb"), owns) == nil {
		t.Error("a notebook outside is refused too")
	}
}

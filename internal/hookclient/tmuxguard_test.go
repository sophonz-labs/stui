package hookclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTmuxKillReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux")
	}
	t.Setenv("TMUX_TMPDIR", "")
	uid := os.Getuid()
	own := filepath.Join("/tmp", "tmux-"+itoa(uid), "default")
	made := t.TempDir()
	for _, c := range []struct {
		cmd  string
		deny bool
	}{
		// What would stop every session.
		{"env -u TMUX TMUX_TMPDIR=/tmp/orch-test $T kill-server 2>/dev/null; rm -rf /tmp/x", true},
		{"tmux kill-server", true},
		{"/opt/homebrew/bin/tmux kill-server", true},
		{"cd /x && TMUX_TMPDIR=" + made + " tmux kill-server", true},
		{"tmux -S /tmp/strt123/s kill-server", false},
		{"tmux -S " + own + " kill-server", true},
		{"tmux -L stui-test kill-server", false},
		{"tmux -L default kill-server", true},
		{`"$T" -S "$D/sock" kill-server`, false},
		{"pkill -f tmux", true},
		{"killall tmux", true},
		{"pkill -f orchestrate.ts", false},
		{`git commit -m "tmux kill-server is refused now"`, false},
		{"python3 - <<'EOF'\nos.system('tmux kill-server')\nEOF\necho done", false},
		{"grep -rn kill-server internal", false},
		{"tmux kill-session -t test", false},
		{"ls; tmux -S /tmp/a/s kill-server; tmux kill-server", true},
	} {
		got := tmuxKillReason(c.cmd, own)
		if (got != "") != c.deny {
			t.Errorf("%q: %q", c.cmd, got)
		}
	}
}

func TestTmuxGuardAnswersClaude(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "tmux kill-server"}})
	out := tmuxGuard("claude", "PreToolUse", raw, "/tmp/tmux-501/default")
	var d struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &d) != nil || d.H.Decision != "deny" || !strings.Contains(d.H.Reason, "-S <socket>") {
		t.Fatalf("%s", out)
	}
	if tmuxGuard("claude", "PreToolUse", []byte(`{"tool_name":"Edit","tool_input":{"file_path":"x"}}`), "") != nil {
		t.Fatal("an edit was judged")
	}
	if tmuxGuard("codex", "PreToolUse", raw, "") != nil || tmuxGuard("claude", "PostToolUse", raw, "") != nil {
		t.Fatal("judged outside Claude's PreToolUse")
	}
}

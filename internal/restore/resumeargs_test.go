package restore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// What a resume line keeps of the agent's first command line, as ps gave it
// (words joined by spaces), and what it names as left out.
func TestResumeOptions(t *testing.T) {
	dir := t.TempDir()
	spaced := filepath.Join(dir, "Application Support", "plug")
	if err := os.MkdirAll(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	os.MkdirAll(other, 0o755)
	settings := filepath.Join(dir, "s.json")
	os.WriteFile(settings, []byte("{}"), 0o644)
	for _, c := range []struct {
		name, kind, args string
		kept, left       string
	}{
		{"nothing to keep", "claude", "claude", "", ""},
		{"model, effort and permissions", "claude", "/Users/me/.local/bin/claude --model opus --effort high --permission-mode acceptEdits --dangerously-skip-permissions",
			"--model 'opus' --effort 'high' --permission-mode 'acceptEdits' --dangerously-skip-permissions", ""},
		{"= form", "claude", "claude --model=sonnet --effort=max", "--model 'sonnet' --effort 'max'", ""},
		{"the old resume, its id and the prompt go", "claude", "claude --dangerously-skip-permissions --resume 05941add-ffbd fix the login bug",
			"--dangerously-skip-permissions", ""},
		{"-c, -p, --session-id, --fork-session go unnamed", "claude", "claude -c -p --session-id 0f0f --fork-session --model opus",
			"--model 'opus'", ""},
		{"-r with no value", "claude", "claude -r --model opus", "--model 'opus'", ""},
		{"a path ps split at its space", "claude", "claude --dangerously-skip-permissions --plugin-dir " + spaced + " --resume x",
			"--dangerously-skip-permissions --plugin-dir '" + spaced + "'", ""},
		{"a path that is not here", "claude", "claude --plugin-dir /nowhere/at/all --model opus", "--model 'opus'", "--plugin-dir"},
		{"many dirs, then the prompt", "claude", "claude --add-dir " + other + " " + spaced + " fix it", "--add-dir '" + other + "' '" + spaced + "'", ""},
		{"settings as a file", "claude", "claude --settings " + settings, "--settings '" + settings + "'", ""},
		{"settings as JSON with no space", "claude", `claude --settings {"model":"opus"}`, `--settings '{"model":"opus"}'`, ""},
		{"settings as JSON ps split", "claude", `claude --settings {"a": 1}`, `--settings '{"a": 1}'`, ""},
		{"a tool list", "claude", "claude --allowedTools Edit,Read --model opus", "--allowedTools 'Edit,Read' --model 'opus'", ""},
		{"a tool list ps split inside a rule", "claude", "claude --allowedTools Bash(git *) Edit", "", "--allowedTools"},
		{"free text is never kept", "claude", "claude --append-system-prompt be terse --system-prompt you are --model opus", "--model 'opus'", "--append-system-prompt --system-prompt"},
		{"a new worktree is not made again", "claude", "claude -w feat --tmux --model opus", "--model 'opus'", "-w --tmux"},
		{"the same option twice, once", "claude", "claude --dangerously-skip-permissions --dangerously-skip-permissions", "--dangerously-skip-permissions", ""},
		{"through node", "claude", "node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js --model opus", "--model 'opus'", ""},
		{"a quote in a value is quoted", "claude", "claude --model it's", `--model 'it'\''s'`, ""},

		{"codex, short forms to long", "codex", "codex -m gpt-5 -s workspace-write -a on-request --search", "--model 'gpt-5' --sandbox 'workspace-write' --ask-for-approval 'on-request' --search", ""},
		{"codex resume <id> and its prompt", "codex", "/Users/me/.local/bin/codex resume 0199-ab --model o3 go on", "--model 'o3'", ""},
		{"codex --last goes", "codex", "codex resume --last --dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-approvals-and-sandbox", ""},
		{"codex -c key=value", "codex", "codex -c model_reasoning_effort=high -c bad", "--config 'model_reasoning_effort=high'", "-c"},
		{"codex -c with quotes is not read back", "codex", `codex -c model="o3"`, "", "-c"},
		{"codex --cd and --worktree are not kept", "codex", "codex -C /tmp --worktree --model o3", "--model 'o3'", "-C --worktree"},
		{"codex --add-dir", "codex", "codex --add-dir " + other, "--add-dir '" + other + "'", ""},

		{"another program", "claude", "vim notes.md", "", ""},
		{"not an agent stui resumes", "gemini", "gemini --model x", "", ""},
	} {
		kept, left := resumeOptions(c.kind, c.args)
		if got := strings.Join(kept, " "); got != c.kept {
			t.Errorf("%s: kept %q, want %q", c.name, got, c.kept)
		}
		if got := strings.Join(left, " "); got != c.left {
			t.Errorf("%s: left %q, want %q", c.name, got, c.left)
		}
	}
}

// The line as a restore types it: the conversation, then the options.
func TestResumeWith(t *testing.T) {
	line, left := resumeWith(&Agent{Kind: "codex", ID: "t1"}, "codex -m o3 --oss --frobnicate")
	if line != "codex resume t1 --model 'o3' --oss" || !reflect.DeepEqual(left, []string{"--frobnicate"}) {
		t.Errorf("%q %q", line, left)
	}
	if line, _ := resumeWith(&Agent{Kind: "claude"}, "claude --model opus"); line != "claude --continue --model 'opus'" {
		t.Errorf("%q", line)
	}
}

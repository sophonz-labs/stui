// Package hookclient is `stui hook <kind>`: what Claude Code, Codex, Gemini
// and stui's opencode plugin run on each hook. It forwards a compact event to the local agent and gets
// out of the way. It always exits 0 -- a hook that failed would show up in
// the user's session -- and prints only a decision stui makes on purpose: an
// edit outside a worker's paths (owns.go), a command that would stop tmux
// (tmuxguard.go), a goal's turn kept going or a loop stopped (stop.go).
// What it did instead goes in a record stui hooks status reads (lastrun.go).
package hookclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Event is what reaches the agent's POST /api/hook.
type Event struct {
	Kind   string         `json:"kind"`
	Pane   string         `json:"pane"`
	Socket string         `json:"socket,omitempty"`
	Event  string         `json:"event"`
	At     int64          `json:"at"`
	Data   map[string]any `json:"data"`
	// Played back from the spool, not heard as it happened (spool.go).
	Replay bool `json:"replay,omitempty"`
	// From a Claude Code background session, outside tmux: Pane is empty
	// and the agent finds the pane showing it (watch/bgsession.go).
	Background bool `json:"background,omitempty"`
}

// kept are the fields of a hook's input the agent uses. Anything else --
// prompts, file contents, the transcript's text -- stays where it was. The
// transcript's path and the working directory (paths, below) go to stui on
// this machine and no further: stui keeps them in memory for the chat
// view, never in its state file or its events.
var kept = []string{
	"hook_event_name", "session_id", "notification_type", "message", "title",
	"tool_name", "agent_id", "agent_type", "teammate_name", "team_name",
	"task_subject", "task_id", "stop_hook_active", "source", "reason",
	"permission_mode", "prompt_id", "error", "error_type", "tool_use_id",
	// Codex notify
	"type", "turn-id", "thread-id", "last-assistant-message",
}

const maxInput = 4000

// paths are kept whole or not at all: a cut path names another file.
var paths = []string{"transcript_path", "cwd"}

const maxPath = 4096

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Not in the middle of a UTF-8 sequence.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// clipRunes cuts s to n characters, the last of them an ellipsis.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Reduce keeps what the agent needs of one hook input.
func Reduce(kind string, raw []byte) (event string, data map[string]any) {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return "", nil
	}
	data = map[string]any{}
	for _, k := range kept {
		if v, ok := in[k]; ok {
			if s, ok := v.(string); ok {
				v = clip(s, 500)
			}
			data[k] = v
		}
	}
	for _, k := range paths {
		if v, ok := in[k].(string); ok && len(v) <= maxPath && filepath.IsAbs(v) && !strings.ContainsRune(v, 0) {
			if k == "transcript_path" && !strings.HasSuffix(v, ".jsonl") {
				continue
			}
			data[k] = v
		}
	}
	tool, _ := in["tool_name"].(string)
	spawn := tool == "Agent" || tool == "Task"
	// A subagent being started (the Agent tool, Task before 2.1.x): its
	// type and its short description, never the prompt it was given.
	if m, ok := in["tool_input"].(map[string]any); ok && spawn {
		m = maps.Clone(m)
		delete(m, "prompt")
		in["tool_input"] = m
		if d, ok := m["description"].(string); ok {
			data["agent_desc"] = clipRunes(strings.Join(strings.Fields(d), " "), 80)
		}
		if t, ok := m["subagent_type"].(string); ok {
			data["agent_subtype"] = clip(t, 100)
		}
		if n, ok := m["name"].(string); ok {
			data["agent_name"] = clip(n, 100)
		}
	}
	// Its PostToolUse names the run it started (a background one's at once).
	if r, ok := in["tool_response"].(map[string]any); ok && spawn {
		if id, ok := r["agentId"].(string); ok {
			data["agent_ref"] = clip(id, 200)
		}
	}
	// What a permission is for: the command, the file, the plan. Kept as
	// JSON text, cut short, since only a summary is ever shown.
	if ti, ok := in["tool_input"]; ok {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(ti) == nil {
			data["tool_input"] = clip(strings.TrimSpace(buf.String()), maxInput)
		}
		// The one line a phone shows for it.
		if m, ok := ti.(map[string]any); ok {
			for _, k := range []string{"command", "file_path", "plan", "url", "pattern", "description"} {
				v, ok := m[k].(string)
				// Codex gives a command as its argv.
				if argv, isList := m[k].([]any); isList && len(argv) > 0 {
					parts := make([]string, 0, len(argv))
					for _, a := range argv {
						if s, ok := a.(string); ok {
							parts = append(parts, s)
						}
					}
					v, ok = strings.Join(parts, " "), true
				}
				if ok && v != "" {
					data["tool_summary"] = clip(v, maxInput)
					break
				}
			}
		}
	}
	// Claude submits a background agent's result to the main agent as a
	// prompt of its own ("<task-notification>..."), with UserPromptSubmit.
	// The prompt stays here; whether it was one is what the agent needs.
	if p, ok := in["prompt"].(string); ok && strings.HasPrefix(strings.TrimSpace(p), "<task-notification>") {
		data["task_notification"] = true
	}
	event, _ = in["hook_event_name"].(string)
	// Codex's notify program gets {"type": "agent-turn-complete", ...};
	// its hooks name their event like Claude's.
	if kind == "codex" && event == "" {
		event, _ = in["type"].(string)
	}
	return event, data
}

type agentRecord struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func stuiHome() string {
	if d := os.Getenv("STUI_HOME"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, ".stui")
}

// Run is stui hook. args are what follows "hook".
func Run(args []string, stdin io.Reader) {
	if len(args) == 0 {
		return
	}
	kind := args[0]
	// stui hooks status running the hook the way the agent would, to see
	// that it starts at all: the one time stui hook prints.
	if os.Getenv("STUI_HOOK_PROBE") != "" {
		fmt.Println(ProbeReply)
		return
	}
	home := stuiHome()
	now := time.Now().UnixMilli()
	pane := os.Getenv("TMUX_PANE")
	// A Claude Code background session runs under Claude's daemon, outside
	// tmux, and is shown in a pane by a Claude there: the agent finds it.
	background := pane == "" && kind == "claude" && os.Getenv("CLAUDE_CODE_SESSION_KIND") == "bg"
	var raw []byte
	// opencode's plugin and Pi's extension give their event the same way: a
	// module that writes to a pipe can be gone before the write is.
	if notify := notifyArg(args[1:]); (kind == "codex" || kind == "opencode" || kind == "pi") && notify != "" {
		raw = []byte(notify)
	} else {
		raw, _ = io.ReadAll(io.LimitReader(stdin, 1<<20))
	}
	event, data := Reduce(kind, raw)
	if pane == "" && !background {
		// Not in tmux: there is no pane to say this happened in.
		noteRun(home, kind, LastRun{At: now, Event: event, Outcome: OutsideTmux})
		return
	}
	if data == nil {
		noteRun(home, kind, LastRun{At: now, Outcome: Unreadable, Pane: pane})
		return
	}
	// A worker's edit outside the paths it owns is refused here, before it
	// happens (owns.go). Only a pane in tmux has owned paths.
	if !background {
		decision := ownsCheck(kind, event, raw, paneOwns(pane))
		// And no command that would stop the tmux server this agent is
		// in (tmuxguard.go).
		if decision == nil {
			socket, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
			decision = tmuxGuard(kind, event, raw, socket)
		}
		// A Stop keeps a goal's turn going, or ends a loop the usage guard
		// stops (stop.go).
		if decision == nil {
			decision = stopDecision(kind, event, raw, paneGoal(pane), homeGuard(home))
		}
		writeDecision(os.Stdout, decision)
	}
	// Where this Claude keeps its teams (~/.claude/teams), when not the
	// default: the agent reads them to group a team's panes.
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" && kind == "claude" {
		data["config_dir"] = d
	}
	socket, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	ev := Event{
		Kind: kind, Pane: pane, Socket: socket, Event: event,
		At: time.Now().UnixMilli(), Data: data, Background: background,
	}
	body, _ := json.Marshal(ev)
	outcome := Delivered
	if status := send(body); status < 200 || status > 299 {
		if spooled(ev.Event) {
			Spool(home, ev)
			outcome = Spooled
		} else {
			// A tool hook is not kept (it is old news by the time an agent
			// could play it back), but it is counted.
			noteDropped(home, ev.Event)
			outcome = Dropped
		}
	}
	noteRun(home, kind, LastRun{At: now, Event: event, Outcome: outcome, Pane: pane})
}

// notifyArg is the JSON Codex's notify program is given as its last
// argument, or "" when there is none. A hooks.json command carries stui's
// version (`--hv 3`) and its input comes on stdin: the "3" is not it.
func notifyArg(rest []string) string {
	var pos []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--hv" {
			i++
			continue
		}
		pos = append(pos, rest[i])
	}
	if len(pos) == 0 {
		return ""
	}
	return pos[len(pos)-1]
}

// ProbeReply is what stui hook prints with STUI_HOOK_PROBE set.
const ProbeReply = "stui hook ok"

// send hands body to the running agent, and says what it answered; 0 when
// there is no agent to answer.
func send(body []byte) int {
	b, err := os.ReadFile(filepath.Join(stuiHome(), "agent.json"))
	if err != nil {
		return 0
	}
	var rec agentRecord
	if json.Unmarshal(b, &rec) != nil || rec.Port == 0 {
		return 0
	}
	return Post(rec.Port, rec.Token, body, 300*time.Millisecond)
}

// Post sends body to the agent on port, giving up after timeout. A refused
// connection -- no agent -- returns at once.
func Post(port int, token string, body []byte, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+itoa(port)+"/api/hook", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-stui-token", token)
	res, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return 0
	}
	res.Body.Close()
	return res.StatusCode
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

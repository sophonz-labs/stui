package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

func spawnHook(w *Watcher, event, tid, typ, desc, ref string) {
	d := map[string]any{"tool_name": "Agent", "tool_use_id": tid, "agent_subtype": typ, "agent_desc": desc}
	if ref != "" {
		d["agent_ref"] = ref
	}
	hook(w, event, d)
}

func items(t *testing.T, w *Watcher) []SubagentItem {
	t.Helper()
	return pane(t, w, "%1").Subagents.Items
}

func TestSubagentItems(t *testing.T) {
	w, _, _ := setup(t, "2.1.288")
	c := withClock(w)
	if b, _ := json.Marshal(pane(t, w, "%1")); !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("no items list: %s", b)
	}
	hook(w, "UserPromptSubmit", nil)
	spawnHook(w, "PreToolUse", "t1", "Explore", "Find API endpoints", "")
	spawnHook(w, "PreToolUse", "t2", "", "Write the tests", "")
	hook(w, "SubagentStart", map[string]any{"agent_id": "a", "agent_type": "Explore"})
	c.add(time.Second)
	hook(w, "SubagentStart", map[string]any{"agent_id": "b", "agent_type": "general-purpose"})
	spawnHook(w, "PostToolUse", "t1", "Explore", "Find API endpoints", "a")
	got := items(t, w)
	if len(got) != 2 || got[0].ID != "a" || got[0].Description != "Find API endpoints" || got[0].Type != "Explore" ||
		got[1].Description != "Write the tests" || got[1].State != SubRunning || got[1].EndedAt != nil {
		t.Fatalf("%+v", got)
	}

	c.add(time.Minute)
	hook(w, "SubagentStop", map[string]any{"agent_id": "a", "agent_type": "Explore"})
	got = items(t, w)
	if got[0].ID != "b" || got[1].ID != "a" || got[1].State != SubDone || got[1].EndedAt == nil ||
		*got[1].EndedAt-got[1].StartedAt != 61000 {
		t.Fatalf("%+v", got)
	}
	if p := pane(t, w, "%1"); p.Subagents.Running != 1 || p.Subagents.Done != 1 {
		t.Fatalf("counts %+v", p.Subagents)
	}

	// A new turn keeps only what still runs.
	hook(w, "Stop", nil)
	hook(w, "UserPromptSubmit", nil)
	if got = items(t, w); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("%+v", got)
	}

	// Silent too long, once it has been heard from: lost; heard from
	// again: running.
	hook(w, "PostToolUse", map[string]any{"agent_id": "b", "tool_name": "Read"})
	c.add(11 * time.Minute)
	w.tick()
	if got = items(t, w); got[0].State != SubLost || got[0].EndedAt == nil {
		t.Fatalf("%+v", got)
	}
	hook(w, "PreToolUse", map[string]any{"agent_id": "b", "tool_name": "Read"})
	if got = items(t, w); got[0].State != SubRunning || got[0].EndedAt != nil {
		t.Fatalf("%+v", got)
	}
	hook(w, "SessionEnd", nil)
}

// Two of a type started together: the PostToolUse that names the run puts
// each description on its own subagent.
func TestSubagentItemsByRef(t *testing.T) {
	w, _, _ := setup(t, "2.1.288")
	hook(w, "UserPromptSubmit", nil)
	spawnHook(w, "PreToolUse", "t1", "Explore", "First", "")
	spawnHook(w, "PreToolUse", "t2", "Explore", "Second", "")
	spawnHook(w, "PostToolUse", "t2", "Explore", "Second", "y")
	hook(w, "SubagentStart", map[string]any{"agent_id": "y", "agent_type": "Explore"})
	hook(w, "SubagentStart", map[string]any{"agent_id": "x", "agent_type": "Explore"})
	desc := map[string]string{}
	for _, it := range items(t, w) {
		desc[it.ID] = it.Description
	}
	if desc["y"] != "Second" || desc["x"] != "First" {
		t.Fatalf("%v", desc)
	}
	// An in-process teammate is named by its id.
	hook(w, "SubagentStart", map[string]any{"agent_id": "researcher@session-be3612de", "agent_type": "general-purpose"})
	for _, it := range items(t, w) {
		if it.ID == "researcher@session-be3612de" && it.Description != "researcher" {
			t.Fatalf("%+v", it)
		}
	}
}

func TestSubagentItemsCapped(t *testing.T) {
	w, _, _ := setup(t, "2.1.288")
	hook(w, "UserPromptSubmit", nil)
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		hook(w, "SubagentStart", map[string]any{"agent_id": id, "agent_type": "Explore"})
		hook(w, "SubagentStop", map[string]any{"agent_id": id, "agent_type": "Explore"})
	}
	hook(w, "SubagentStart", map[string]any{"agent_id": "z", "agent_type": "Explore"})
	got := items(t, w)
	if len(got) != maxSubItems || got[0].ID != "z" {
		t.Fatalf("%d %+v", len(got), got)
	}
}

func TestParseTeamArgs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "teams", "teammate-argv.txt"))
	if err != nil {
		t.Fatal(err)
	}
	a := parseTeamArgs(strings.Fields(string(b)))
	if a == nil || a.team != "session-be3612de" || a.member != "researcher" || a.parent != "be3612de-d629-4413-9df3-2094e60bda57" {
		t.Fatalf("%+v", a)
	}
	if a := parseTeamArgs([]string{"claude", "--agent-id=x@t", "--team-name=t", "--agent-name=x"}); a == nil || a.team != "t" || a.member != "x" {
		t.Fatalf("%+v", a)
	}
	if parseTeamArgs([]string{"claude", "--resume", "abc"}) != nil {
		t.Fatal("not a teammate")
	}
}

const leadSession = "be3612de-d629-4413-9df3-2094e60bda57"

// A team in split panes: the lead in %1, a teammate in %3 (from the team
// config) and one in %4 known only from its command line.
func teamSetup(t *testing.T) (*Watcher, *fakeTmux, string) {
	t.Helper()
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "teams", "session-be3612de")
	os.MkdirAll(teamDir, 0o755)
	b, _ := os.ReadFile(filepath.Join("testdata", "teams", "config.json"))
	os.WriteFile(filepath.Join(teamDir, "config.json"), b, 0o644)
	argv, _ := os.ReadFile(filepath.Join("testdata", "teams", "teammate-argv.txt"))
	f := &fakeTmux{
		panes: "%1|~|work|~||~|0|~|0|~|2.1.288|~|api|~|/dev/ttys001|~|lead\n" +
			"%3|~|work|~||~|0|~|1|~|2.1.288|~|api|~|/dev/ttys003|~|researcher\n" +
			"%4|~|work|~||~|0|~|2|~|2.1.288|~|api|~|/dev/ttys004|~|reviewer\n" +
			"%5|~|work|~||~|1|~|0|~|2.1.288|~|solo|~|/dev/ttys005|~|solo\n",
		screens: map[string]string{"%1": "", "%3": "", "%4": "", "%5": ""},
	}
	reviewer := strings.ReplaceAll(strings.ReplaceAll(string(argv), "researcher", "reviewer"), "agent-type Explore", "agent-type Plan")
	w := New(Options{Tmux: f, ClaudeDir: dir, Args: func(tty string) []string {
		if tty == "/dev/ttys004" {
			return strings.Fields(reviewer)
		}
		return []string{"claude"}
	}})
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: "SessionStart", Data: map[string]any{"session_id": leadSession}})
	w.poll(context.Background())
	return w, f, teamDir
}

func TestTeamPanes(t *testing.T) {
	w, f, teamDir := teamSetup(t)
	lead, mate, other, solo := pane(t, w, "%1"), pane(t, w, "%3"), pane(t, w, "%4"), pane(t, w, "%5")
	if lead.Team == nil || *lead.Team != (Team{Name: "session-be3612de", Role: RoleLead, Member: "team-lead", LeadPane: "%1"}) {
		t.Fatalf("lead %+v", lead.Team)
	}
	if mate.Team == nil || *mate.Team != (Team{Name: "session-be3612de", Role: RoleTeammate, Member: "researcher", LeadPane: "%1"}) {
		t.Fatalf("teammate %+v", mate.Team)
	}
	if other.Team == nil || other.Team.Member != "reviewer" || other.Team.LeadPane != "%1" || other.Team.Ended {
		t.Fatalf("by args %+v", other.Team)
	}
	if solo.Team != nil {
		t.Fatalf("solo %+v", solo.Team)
	}
	if b, _ := json.Marshal(solo); strings.Contains(string(b), `"team"`) {
		t.Fatalf("team on a solo pane: %s", b)
	}
	if s := w.Snapshot().Sessions[0]; s.TeamEnded != 0 {
		t.Fatalf("%+v", s)
	}

	// The team directory goes. Missing at one poll may be a rewrite.
	os.RemoveAll(teamDir)
	w.poll(context.Background())
	if p := pane(t, w, "%3"); p.Team == nil || p.Team.Ended {
		t.Fatalf("ended at the first poll without its config: %+v", p.Team)
	}
	// The lead's session ends, and its pane is a shell again. Its teammates
	// are orphaned.
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: "SessionEnd", Data: map[string]any{"session_id": leadSession}})
	f.mu.Lock()
	f.panes = strings.Replace(f.panes, "0|~|2.1.288|~|api|~|/dev/ttys001|~|lead", "0|~|zsh|~|api|~|/dev/ttys001|~|lead", 1)
	f.mu.Unlock()
	w.poll(context.Background())
	mate, other = pane(t, w, "%3"), pane(t, w, "%4")
	if mate.Team == nil || !mate.Team.Ended || mate.Team.Member != "researcher" {
		t.Fatalf("teammate %+v", mate.Team)
	}
	if other.Team == nil || !other.Team.Ended {
		t.Fatalf("by args %+v", other.Team)
	}
	if s := w.Snapshot().Sessions[0]; s.TeamEnded != 2 {
		t.Fatalf("%+v", s)
	}
}

// The lead killed outright leaves its team directory behind; its pane gone
// says the team is over.
func TestTeamLeadPaneGone(t *testing.T) {
	w, f, _ := teamSetup(t)
	f.mu.Lock()
	f.panes = strings.SplitN(f.panes, "\n", 2)[1]
	f.mu.Unlock()
	w.poll(context.Background())
	if p := pane(t, w, "%3"); p.Team == nil || !p.Team.Ended {
		t.Fatalf("%+v", p.Team)
	}
}

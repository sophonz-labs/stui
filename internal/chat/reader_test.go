package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func lineTexts(ls []Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, string(l.Data))
	}
	return out
}

// A line still being written is held until its newline; a record over
// 2 MB is dropped, whether it arrives at once or in pieces.
func TestFollowerPartialAndOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, path, "one\n")
	f, err := NewFollower(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, path, "two\nthr")
	if f.Check() != Grew {
		t.Fatal("no growth seen")
	}
	ls, _ := f.Read()
	if strings.Join(lineTexts(ls), "|") != "two" || ls[0].Off != 4 || f.Offset() != 8 {
		t.Fatalf("%v %d", lineTexts(ls), f.Offset())
	}
	appendTo(t, path, "ee\r\n")
	ls, _ = f.Read()
	if strings.Join(lineTexts(ls), "|") != "three" || ls[0].Off != 8 {
		t.Fatalf("%v", lineTexts(ls))
	}
	// Over the limit, in two pieces, then a normal line.
	big := strings.Repeat("x", MaxRecord)
	appendTo(t, path, big)
	if ls, _ := f.Read(); len(ls) != 0 {
		t.Fatal("half a record read")
	}
	appendTo(t, path, "yy\nfour\n")
	ls, _ = f.Read()
	if strings.Join(lineTexts(ls), "|") != "four" {
		t.Fatalf("%d lines, first %d bytes", len(ls), len(ls[0].Data))
	}
	if f.Check() != Same {
		t.Fatal("changed?")
	}
}

// Replaced by another file, cut short, or rewritten in place: start over.
func TestFollowerReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	appendTo(t, path, "a\nb\n")
	f, _ := NewFollower(path, 4)
	if f.Verify() != Same {
		t.Fatal("not same")
	}
	// Rewritten in place, same length.
	os.WriteFile(path, []byte("a\nc\n"), 0o600)
	if f.Verify() != Replaced {
		t.Fatal("rewrite not seen")
	}
	if _, err := f.Read(); err != ErrReplaced {
		t.Fatal(err)
	}
	// Truncated.
	f, _ = NewFollower(path, 4)
	os.WriteFile(path, []byte("a\n"), 0o600)
	if f.Check() != Replaced {
		t.Fatal("truncation not seen")
	}
	// Another file moved into its place.
	os.WriteFile(path, []byte("a\nc\n"), 0o600)
	f, _ = NewFollower(path, 4)
	other := filepath.Join(dir, "o.jsonl")
	os.WriteFile(other, []byte("a\nc\nd\n"), 0o600)
	os.Rename(other, path)
	if f.Check() != Replaced {
		t.Fatal("rotation not seen")
	}
	os.Remove(path)
	if f.Check() != Gone {
		t.Fatal("removal not seen")
	}
}

func TestReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	var want []string
	var b strings.Builder
	for i := range 3000 {
		s := fmt.Sprintf(`{"n":%d,"pad":"%s"}`, i, strings.Repeat("p", i%97))
		want = append(want, s)
		b.WriteString(s + "\n")
		if i == 1500 {
			b.WriteString(strings.Repeat("z", MaxRecord+10) + "\n")
		}
	}
	b.WriteString("partial")
	os.WriteFile(path, []byte(b.String()), 0o600)
	fh, _ := os.Open(path)
	defer fh.Close()
	st, _ := fh.Stat()
	end := lineEnd(fh, st.Size())
	if end != st.Size()-int64(len("partial")) {
		t.Fatalf("end %d", end)
	}
	var got []string
	readBack(fh, end, func(l Line) bool {
		got = append(got, string(l.Data))
		return true
	})
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[len(want)-1-i] {
			t.Fatalf("line %d: %.40q", i, got[i])
		}
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	var b strings.Builder
	// Turn A: a prompt and 10 replies; turn B: a prompt and 30 tool calls.
	rec := func(typ, id, content string) {
		fmt.Fprintf(&b, `{"type":%q,"uuid":%q,"timestamp":"2026-01-01T00:00:00Z","message":{"role":%q,"content":%s,"stop_reason":"tool_use"}}`+"\n", typ, id, typ, content)
	}
	rec("user", "A", `"first"`)
	for i := range 10 {
		rec("assistant", fmt.Sprintf("a%d", i), `[{"type":"text","text":"x"}]`)
	}
	rec("user", "B", `"second"`)
	for i := range 30 {
		rec("assistant", fmt.Sprintf("b%d", i), `[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"true"}}]`)
	}
	os.WriteFile(path, []byte(b.String()), 0o600)

	w, dec, err := ReadTail(path, "claude", 0, 10)
	if err != nil || dec == nil {
		t.Fatal(err)
	}
	if len(w.Messages) != 10 || !w.HasMore || w.Messages[0].ID != "b20" || w.Messages[9].ID != "b29" {
		t.Fatalf("%d %v %s", len(w.Messages), w.HasMore, w.Messages[0].ID)
	}
	// Their turn is known though its prompt is not among them.
	if w.Messages[0].Turn != "B" || w.Turn == nil || w.Turn.ID != "B" || w.Turn.State != TurnWorking {
		t.Fatalf("%+v %+v", w.Messages[0], w.Turn)
	}
	// The page above.
	up, _, err := ReadTail(path, "claude", w.Before, 25)
	if err != nil || len(up.Messages) != 25 || up.Messages[24].ID != "b19" || !up.HasMore {
		t.Fatalf("%v %d", err, len(up.Messages))
	}
	top, _, _ := ReadTail(path, "claude", up.Before, 100)
	if len(top.Messages) != 7 || top.Messages[0].ID != "A" || top.HasMore {
		t.Fatalf("%d %v", len(top.Messages), top.HasMore)
	}
	if _, _, err := ReadTail(path, "claude", w.Before+3, 10); err != ErrBadOffset {
		t.Fatalf("mid-record offset: %v", err)
	}
	if _, _, err := ReadTail(path, "gemini", 0, 10); err == nil {
		t.Fatal("unsupported agent read")
	}
}

func TestClip(t *testing.T) {
	m := Message{Blocks: []Block{
		{Type: BlockText, Text: strings.Repeat("가", 70000)},
		{Type: BlockToolResult, Output: strings.Repeat("é", 5000)},
		{Type: BlockToolCall, Input: map[string]any{"command": strings.Repeat("c", 5000), "list": make([]any, 30)}},
		{Type: BlockText, Text: "short"},
	}}
	c := Clip(m, Wire)
	if b := c.Blocks[0]; !b.Clipped || b.Size != 70000 || utf8.RuneCountInString(b.Text) != 64000 || !utf8.ValidString(b.Text) {
		t.Fatalf("%v %d", b.Clipped, b.Size)
	}
	if b := c.Blocks[1]; !b.Clipped || b.Size != 5000 || utf8.RuneCountInString(b.Output) != 4000 {
		t.Fatal("tool output")
	}
	in := c.Blocks[2].Input.(map[string]any)
	if !c.Blocks[2].Clipped || len(in["command"].(string)) > 4100 {
		t.Fatalf("%v", c.Blocks[2].Clipped)
	}
	if b := c.Blocks[3]; b.Clipped || b.Size != 0 {
		t.Fatal("short text clipped")
	}
	if utf8.RuneCountInString(m.Blocks[0].Text) != 70000 {
		t.Fatal("the original was changed")
	}
	if s := summarize(map[string]any{"command": "a\nb"}); s != "a…" {
		t.Fatalf("%q", s)
	}
	if s := summarize(`{"cmd":["git","status"]}`); s != "git status" {
		t.Fatalf("%q", s)
	}
}

func TestLocate(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	sid := "5f1c2a9e-0000-4000-8000-000000000001"
	p := filepath.Join(claude, "projects", "-home-u-p", sid+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("{}\n"), 0o600)

	if got := Locate(Source{Agent: "claude", SessionID: sid, ClaudeDirs: []string{claude}}); got != p {
		t.Fatalf("by id: %q", got)
	}
	// The hook's path wins, when it is a transcript.
	named := filepath.Join(home, "other.jsonl")
	os.WriteFile(named, []byte("{}\n"), 0o600)
	if got := Locate(Source{Agent: "claude", SessionID: sid, Path: named, ClaudeDirs: []string{claude}}); got != named {
		t.Fatalf("hook path: %q", got)
	}
	notes := filepath.Join(home, "notes.txt")
	os.WriteFile(notes, []byte("x"), 0o600)
	if got := Locate(Source{Agent: "claude", SessionID: "../x", Path: notes, ClaudeDirs: []string{claude}}); got != "" {
		t.Fatalf("read %q", got)
	}

	// Codex: by id in the name, then by directory and time.
	codex := filepath.Join(home, ".codex")
	day := filepath.Join(codex, "sessions", "2026", "01", "02")
	os.MkdirAll(day, 0o700)
	cid := "01a00000-0000-7000-8000-00000000c0de"
	byID := filepath.Join(day, "rollout-2026-01-02T04-00-00-"+cid+".jsonl")
	os.WriteFile(byID, []byte(`{"type":"session_meta","payload":{"id":"`+cid+`","cwd":"/home/u/a"}}`+"\n"), 0o600)
	byCwd := filepath.Join(day, "rollout-2026-01-02T05-00-00-01a00000-0000-7000-8000-000000000bbb.jsonl")
	os.WriteFile(byCwd, []byte(`{"type":"session_meta","payload":{"id":"x","cwd":"/home/u/b"}}`+"\n"), 0o600)
	if got := Locate(Source{Agent: "codex", SessionID: cid, CodexHome: codex}); got != byID {
		t.Fatalf("codex by id: %q", got)
	}
	since := time.Now().Add(-time.Minute)
	if got := Locate(Source{Agent: "codex", Cwd: "/home/u/b", Since: since, CodexHome: codex}); got != byCwd {
		t.Fatalf("codex by cwd: %q", got)
	}
	if got := Locate(Source{Agent: "codex", Cwd: "/home/u/b", Since: time.Now().Add(time.Hour), CodexHome: codex}); got != "" {
		t.Fatalf("an older rollout: %q", got)
	}
}

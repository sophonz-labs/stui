package orch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteSent(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	at := time.Date(2026, 10, 19, 14, 5, 0, 0, time.Local)
	p1, err := WriteSent("r-dc57e9fc", "ux", []byte("first"), at)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(RunDir("r-dc57e9fc"), "sent", "20261019-140500-ux.md"); p1 != want {
		t.Fatalf("path %s, want %s", p1, want)
	}
	// The same second: -2, and the first is left as it was.
	p2, _ := WriteSent("r-dc57e9fc", "ux", []byte("second"), at)
	if !strings.HasSuffix(p2, "20261019-140500-ux-2.md") {
		t.Fatalf("second in a second: %s", p2)
	}
	if b, _ := os.ReadFile(p1); string(b) != "first" {
		t.Fatalf("first copy written over: %q", b)
	}
	if fi, _ := os.Stat(p1); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	// Outside a run, and a worker named oddly.
	p3, _ := WriteSent("", "%31", []byte("x"), at)
	if p3 != filepath.Join(Dir(), "sent", "20261019-140500-31.md") {
		t.Fatalf("no run: %s", p3)
	}
	if _, err := WriteSent("../r-x", "ux", []byte("x"), at); err == nil {
		t.Fatal("a bad run id was written")
	}
}

func TestSentLine(t *testing.T) {
	got := SentLine("coordinator", "/h/.stui/orch/r-1/sent/a.md", "위임을 기록으로\n남긴다", 4210, "")
	want := `A message from coordinator is in /h/.stui/orch/r-1/sent/a.md ("위임을 기록으로 남긴다", 4.1 KB). Read it and do it.`
	if got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
	got = SentLine("", "/x.md", "T", 300, "3절부터")
	if got != `3절부터 -- A message from your coordinator is in /x.md ("T", 300 B). Read it and do it.` {
		t.Fatal(got)
	}
	if Size(1<<20) != "1.0 MB" || Size(1023) != "1023 B" {
		t.Fatal(Size(1<<20), Size(1023))
	}
}

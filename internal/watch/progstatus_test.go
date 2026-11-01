package watch

import (
	"context"
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	ok := func(body string) statusReport {
		t.Helper()
		r, good := parseStatus(body)
		if !good {
			t.Fatalf("%q dropped", body)
		}
		return r
	}
	// Every key ("Plan", "Apply 3 to add, 1 to change, 0 to destroy?").
	r := ok("state=blocked:kind=permission:progress=42:id=tf/plan:app=terraform:title=UGxhbg==:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/")
	if r.State != "blocked" || r.Kind != "permission" || r.Progress == nil || *r.Progress != 42 || r.id != "tf/plan" ||
		r.App != "terraform" || r.Title != "Plan" || r.Msg != "Apply 3 to add, 1 to change, 0 to destroy?" {
		t.Fatalf("%+v", r)
	}
	if r := ok("state=done:msg=UGxhbg"); r.Msg != "Plan" {
		t.Fatalf("padding is optional: %+v", r)
	}
	if r := ok(" state = working :\tapp=cargo\t"); r.State != "working" || r.App != "cargo" {
		t.Fatalf("whitespace: %+v", r)
	}
	if r := ok("state=idle:app=a:state=done:app=b"); r.State != "done" || r.App != "b" {
		t.Fatalf("last wins: %+v", r)
	}
	if r := ok("garbage:=x:Bad=1:app=b@d:future=yes:state=working:app=ok:::"); r.State != "working" || r.App != "ok" {
		t.Fatalf("bad pairs skipped: %+v", r)
	}
	if r := ok("state=working:kind=auth:progress=10"); r.Kind != "" || r.Progress == nil || *r.Progress != 10 {
		t.Fatalf("kind only when blocked: %+v", r)
	}
	if r := ok("state=done:kind=auth:progress=10"); r.Kind != "" || r.Progress != nil {
		t.Fatalf("progress only working or blocked: %+v", r)
	}
	if r := ok("state=blocked:kind=dance:progress=101"); r.Kind != "" || r.Progress != nil {
		t.Fatalf("unknown kind, out of range: %+v", r)
	}
	for _, body := range []string{
		"?",                                    // the query
		"app=cargo",                            // no state
		"state=bogus",                          // a state from a later revision
		"state=working:averyveryverylongkey=1", // key past 16
		"state=clear:id=a//b",                  // malformed id: never falls back to the root
		"state=working:app=" + strings.Repeat("a", 33),
		"state=working:msg=A",                               // in the alphabet, not base64
		"state=working:msg=AAE=",                            // a control character
		"state=working:title=" + strings.Repeat("QUFB", 65), // decodes past 192
		"state=done:" + strings.Repeat("x", statusMaxBody),  // the body past its limit
	} {
		if r, good := parseStatus(body); good {
			t.Errorf("%.60q kept: %+v", body, r)
		}
	}
}

func TestOSCScannerReports(t *testing.T) {
	var s oscScanner
	_, _, rs := s.scan([]byte("a\x1b]7501;state=working:app=cargo\x1b\\b\x1b]7501;state=blo"))
	if len(rs) != 1 || rs[0].State != "working" || rs[0].App != "cargo" {
		t.Fatalf("%+v", rs)
	}
	_, notes, rs := s.scan([]byte("cked:kind=question\x07\x1b]9;hi\x07"))
	if len(rs) != 1 || rs[0].State != "blocked" || rs[0].Kind != "question" || len(notes) != 1 {
		t.Fatalf("split across chunks: %+v %q", rs, notes)
	}
	// Past oscMax the scanner keeps a prefix only: that is not the report.
	long := "\x1b]7501;state=done:msg=" + strings.Repeat("A", oscMax) + "\x1b\\"
	if _, _, rs := s.scan([]byte(long)); len(rs) != 0 {
		t.Fatalf("truncated report kept: %+v", rs)
	}
}

func osc7501(body string) []byte { return []byte("\x1b]7501;" + body + "\x1b\\") }

// A program that reports is believed over hooks and the screen until it
// clears its status.
func TestReportWins(t *testing.T) {
	w, f, _ := setup(t, "2.1.283")
	if p := pane(t, w, "%1"); p.Reported || p.Report != nil {
		t.Fatalf("%+v", p)
	}
	w.Output("%1", osc7501("state=blocked:kind=question:app=claude-code:msg=V2hpY2ggb25lPw"))
	p := pane(t, w, "%1")
	if p.State != Waiting || !p.Reported || p.Report == nil || p.Report.State != "blocked" || p.Report.Kind != "question" ||
		p.Report.Msg != "Which one?" || p.Report.At == 0 || p.Source != srcReport {
		t.Fatalf("%+v %+v", p, p.Report)
	}
	// The hook and the screen say working: the report still stands.
	f.setScreen("%1", screen(t, "claude-working.txt"))
	hook(w, "UserPromptSubmit", nil)
	w.Output("%1", []byte("x"))
	w.captureNow("%1")
	if p := pane(t, w, "%1"); p.State != Waiting || !p.Reported {
		t.Fatalf("overridden: %+v", p)
	}
	w.Output("%1", osc7501("state=done"))
	if p := pane(t, w, "%1"); p.State != Done || p.Report.State != "done" || p.Report.Kind != "" {
		t.Fatalf("%+v", p)
	}
	w.Output("%1", osc7501("state=error"))
	if p := pane(t, w, "%1"); p.State != Error {
		t.Fatalf("%+v", p)
	}
	// A broken report and a record of a part change nothing.
	w.Output("%1", osc7501("state=bogus"))
	w.Output("%1", osc7501("state=working:id=build"))
	if p := pane(t, w, "%1"); p.State != Error || p.Report.State != "error" {
		t.Fatalf("%+v", p)
	}
	// Cleared: the screen decides again.
	w.Output("%1", osc7501("state=clear"))
	eventually(t, "screen again", func() bool { p := pane(t, w, "%1"); return !p.Reported && p.State == Working })
}

// A plain program that reports is listed while it runs, and not after.
func TestReportFromPlainProgram(t *testing.T) {
	w, f, _ := setup(t, "2.1.283")
	for _, p := range w.Snapshot().Panes {
		if p.ID == "%2" {
			t.Fatal("a shell listed")
		}
	}
	f.mu.Lock()
	f.panes = strings.Replace(f.panes, "|~|zsh|~|sh|~|", "|~|cargo|~|sh|~|", 1)
	f.mu.Unlock()
	w.poll(context.Background())
	w.Output("%2", osc7501("state=working:app=cargo:progress=40"))
	p := pane(t, w, "%2")
	if p.State != Working || !p.Reported || *p.Report.Progress != 40 || p.Agent != "" {
		t.Fatalf("%+v", p)
	}
	// cargo exits: the report went with it.
	f.mu.Lock()
	f.panes = strings.Replace(f.panes, "|~|cargo|~|sh|~|", "|~|zsh|~|sh|~|", 1)
	f.mu.Unlock()
	w.poll(context.Background())
	for _, p := range w.Snapshot().Panes {
		if p.ID == "%2" {
			t.Fatalf("still listed: %+v", p)
		}
	}
}

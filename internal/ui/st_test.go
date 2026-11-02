package ui

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeToken = "tok-3f9c-never-shown"

// st's /api/agents as it answers, trimmed: fields stui does not read
// (source, phase, about, team…) stay in, as st sends them.
const stAgentsJSON = `{"v":3,"seq":7,"at":1,"hooks":{},"sessions":[{"name":"work","state":"working","working":1}],
"panes":[
 {"id":"%1","session":"work","window":0,"windowName":"main","pane":0,"agent":"claude","command":"claude","state":"working","since":1,"source":"hook","phase":"tool","icon":"claude","title":"lead",
  "orch":{"run":"big","role":"coordinator","name":"","done":false,"adopted":false,"about":"x","team":"t","inRun":true,"briefState":"ok"}},
 {"id":"%3","session":"work","window":1,"windowName":"w","pane":0,"agent":"codex","command":"codex","state":"idle","since":1,
  "orch":{"run":"big","role":"worker","parent":"%1","name":"api","done":true,"summary":"api done"}},
 {"id":"%2","session":"work","window":2,"windowName":"x","pane":0,"agent":"claude","state":"idle","since":1}
]}`

// fakeST is st's server: it answers GETs with the token, and counts every
// request that is not a GET.
func fakeST(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		if r.Header.Get(stTokenHeader) != fakeToken {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/agents":
			fmt.Fprint(w, stAgentsJSON)
		case "/api/panes/history":
			fmt.Fprint(w, `{"text":"st's pane\n"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &writes
}

// stAt writes st's agent.json for srv (pid: this test, which is alive).
func stAt(t *testing.T, srv *httptest.Server, pid int) {
	dir := t.TempDir()
	t.Setenv("STUI_ST_HOME", dir)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	os.WriteFile(filepath.Join(dir, "agent.json"), []byte(fmt.Sprintf(`{"pid":%d,"port":%d,"token":%q,"session":"s"}`, pid, port, fakeToken)), 0o600)
}

func TestReadST(t *testing.T) {
	srv, writes := fakeST(t)
	stAt(t, srv, os.Getpid())
	c, ps := readST()
	if c == nil || len(ps) != 3 || !ps[0].st || ps[0].Orch == nil || ps[0].Orch.Run != "big" || ps[1].Orch.Parent != "%1" {
		t.Fatalf("readST: %v %+v", c, ps)
	}
	if err := c.do("POST", "/api/action", map[string]string{}, nil); err != errReadOnly || writes.Load() != 0 {
		t.Fatalf("a POST to st: %v, %d writes reached it", err, writes.Load())
	}

	stAt(t, srv, 0) // no live process: st is not running
	if c, _ := readST(); c != nil {
		t.Fatal("read st with a dead pid")
	}
	srv.Close() // a pid that lives, a server that does not answer
	stAt(t, srv, os.Getpid())
	start := time.Now()
	if c, _ := readST(); c != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a closed server: %v after %v", c, time.Since(start))
	}
	t.Setenv("STUI_ST_HOME", t.TempDir()) // no agent.json
	if c, _ := readST(); c != nil {
		t.Fatal("read st without agent.json")
	}
}

// stApp is stui with one run of its own and st's three panes merged in.
func stApp(t *testing.T) (*app, *atomic.Int32) {
	srv, writes := fakeST(t)
	stAt(t, srv, os.Getpid())
	s := &snap{agents: agents{Panes: []agentPane{
		{ID: "%1", Session: "work", State: "working", Agent: "claude"},                                // st's coordinator, seen by stui as an agent
		{ID: "%2", Session: "work", State: "idle", Orch: &orchInfo{Run: "mine", Role: "coordinator"}}, // stui's own run; st sees a plain agent
		{ID: "%3", Session: "other", State: "idle", Orch: &orchInfo{Run: "big", Role: "coordinator"}}, // same id, another session: another pane
	}}}
	var st []agentPane
	s.st, st = readST()
	s.mergeST(st)
	a := &app{snap: s, folded: map[string]bool{}, w: 120, h: 30, stack: []*page{{v: orchView{}}}, later: make(chan func(), 16), refresh: make(chan struct{}, 1)}
	a.readST.Store(true)
	return a, writes
}

func TestMergeST(t *testing.T) {
	a, _ := stApp(t)
	ps := a.snap.agents.Panes
	if len(ps) != 4 {
		t.Fatalf("%d panes, want 4: %+v", len(ps), ps)
	}
	if !ps[0].st || ps[0].Orch == nil || ps[0].Orch.Run != "big" {
		t.Errorf("%%1 is st's record: %+v", ps[0])
	}
	if ps[1].st || ps[1].Orch.Run != "mine" {
		t.Errorf("%%2 keeps stui's run: %+v", ps[1])
	}
	if ps[2].st || ps[2].Session != "other" || !ps[3].st || ps[3].Session != "work" {
		t.Errorf("%%3 in two sessions is two panes: %+v %+v", ps[2], ps[3])
	}
	var names []string
	for _, r := range (orchView{}).rows(a) {
		names = append(names, r.cells[0])
	}
	want := "▾ big| └─ ● coordinator|▾ big (st)| └─ ▾ coordinator|    └─ ● api ✓|▾ mine| └─ ● coordinator"
	if got := strings.Join(names, "|"); got != want {
		t.Errorf("tree:\n%s\nwant\n%s", got, want)
	}
}

func TestSTRowsAreReadOnly(t *testing.T) {
	a, writes := stApp(t)
	rows := (orchView{}).rows(a)
	var lead, mine *row
	for i := range rows {
		switch {
		case rows[i].id == "%1":
			lead = &rows[i]
		case rows[i].id == "%2":
			mine = &rows[i]
		}
	}
	(orchView{}).key(a, "ctrl-d", lead)
	if a.flash != stRefusal || !a.bad || a.mode != normal {
		t.Fatalf("ctrl-d on st's run: %q (mode %v)", a.flash, a.mode)
	}
	(orchView{}).key(a, "ctrl-d", mine)
	if a.mode != confirming {
		t.Fatalf("ctrl-d on stui's own run must still ask")
	}
	a.mode = normal
	// Output comes from st's server, by GET.
	(orchView{}).key(a, "enter", lead)
	pg := a.top().pg
	lines, err := pg.reload()
	if err != nil || len(lines) != 1 || lines[0] != "st's pane" {
		t.Fatalf("st's output: %q %v", lines, err)
	}
	if writes.Load() != 0 {
		t.Fatalf("%d requests that were not GETs reached st", writes.Load())
	}
	a.pop()
	// y copies; the token shows nowhere.
	a.top().sel = "st:big"
	a.normalKey("y")
	if a.copied != "big" {
		t.Errorf("y on st's run: %q", a.copied)
	}
	for _, v := range []view{orchView{}, agentsView{}, panesView{session: "work"}, sessionsView{}} {
		a.stack = []*page{{v: v}}
		if got := strings.Join(a.render(), "\n") + a.flash + fmt.Sprint(a.snap.st); strings.Contains(got, fakeToken) {
			t.Fatalf("%T shows the token", v)
		}
		a.normalKey("Y")
		if strings.Contains(a.copied, fakeToken) {
			t.Fatalf("Y on %T copied the token", v)
		}
	}
	a.stack = []*page{{v: agentsView{}}}
	if got := plain(strings.Join(a.render(), "\n")); !strings.Contains(got, "coordinator big (st)") {
		t.Errorf("agents view does not mark st's:\n%s", got)
	}
}

func TestEmptyOrchSaysWhereSTIs(t *testing.T) {
	old := stOnPath
	stOnPath = func() bool { return true }
	t.Cleanup(func() { stOnPath = old })
	a := &app{snap: &snap{}, folded: map[string]bool{}, w: 120, h: 30, stack: []*page{{v: orchView{}}}}
	a.readST.Store(true)
	if got := plain(strings.Join(a.render(), "\n")); !strings.Contains(got, "its server is not running") {
		t.Errorf("st installed, not running:\n%s", got)
	}
	a.snap.st = &client{}
	if got := plain(strings.Join(a.render(), "\n")); strings.Contains(got, "st is installed") {
		t.Errorf("st running: no hint\n%s", got)
	}
	a.snap.st = nil
	a.readST.Store(false)
	if got := plain(strings.Join(a.render(), "\n")); !strings.Contains(got, "st.read: true") {
		t.Errorf("st.read off:\n%s", got)
	}
}

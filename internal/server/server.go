// Package server is the process stui leaves running on this machine: it
// watches the tmux panes for coding agents (internal/watch), carries the
// orchestration mail (internal/orch), types into panes for the CLI
// (internal/chat), keeps the agents' plan limits (internal/usage) and reads
// a pane's GitHub Actions runs through gh (internal/actions). It answers
// only on 127.0.0.1, and only to a request that carries the token in
// ~/.stui/agent.json.
//
// Nothing it serves reaches past this machine: no account, no remote
// access, no web terminal.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	ghactions "github.com/sophonz-labs/stui/internal/actions"
	"github.com/sophonz-labs/stui/internal/chat"
	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/hooks"
	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/state"
	"github.com/sophonz-labs/stui/internal/usage"
	aw "github.com/sophonz-labs/stui/internal/watch"
)

// Version is the stui version, stamped by main.
var Version = "dev"

// Routes answers the requests it knows, and is false for the rest.
type Routes interface {
	ServeHTTP(http.ResponseWriter, *http.Request) bool
}

type server struct {
	m        *mux.Tmux
	tm       aw.ExecTmux
	watcher  *aw.Watcher
	chat     *chat.Service
	orch     Routes
	usage    *usage.Service
	actions  *ghactions.Service
	hooks    *hookCache
	token    string
	muxVer   string
	shutdown func()
}

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+f+"\n", a...)
}

// Run is stui __serve: it serves until it is stopped, and writes the
// record the CLI finds it by once it listens.
func Run(argv []string) int {
	if s := state.Running(); s != nil {
		fmt.Fprintf(os.Stderr, "stui is already running here (pid %d)\n", s.PID)
		return 1
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	m := mux.NewTmux()
	if !m.Available() {
		fmt.Fprintln(os.Stderr, "stui needs tmux on PATH")
		return 1
	}
	tm := aw.ExecTmux{Bin: m.Command()}
	socket := m.ServerSocket(ctx)

	go refreshSkills(ctx)
	hookState := &hookCache{}
	if changes, err := hooks.AutoUpdate(hooks.DefaultEnv()); err != nil {
		logf("agent watch: could not update outdated hooks: %v", err)
	} else {
		for _, c := range changes {
			logf("agent watch: %s hooks updated from v%d to v%d (backup %s)", c.Name, c.From, c.To, c.Backup)
		}
	}
	var gateTell func(aw.Gate) string
	watcher := aw.New(aw.Options{
		GateFile: filepath.Join(home.Dir(), "orch", "gates.json"),
		GateAnswered: func(g aw.Gate) string {
			if gateTell == nil {
				return "stui cannot type into panes here"
			}
			return gateTell(g)
		},
		Tmux:        tm,
		Socket:      socket,
		Hooks:       hookState.Installed,
		HookStates:  hookState.States,
		HookSets:    func() any { return hookState.Sets() },
		StatePath:   aw.StatePath(home.Dir(), socket),
		SpoolDir:    filepath.Join(home.Dir(), hookclient.SpoolDir),
		Logf:        logf,
		DroppedFile: hookclient.DroppedFile(home.Dir()),
	})
	go watcher.Run(ctx, m.Command())
	go watcher.RecordGates()

	chatOpts := chat.Options{
		Source: func(pane string) (chat.Source, string, bool) {
			t, ok := watcher.Transcript(pane)
			if !ok {
				return chat.Source{}, "", false
			}
			return chat.Source{
				Agent: t.Agent, SessionID: t.SessionID, Path: t.Path, Cwd: t.Cwd, Since: t.Since,
				ClaudeDirs: t.ClaudeDirs, Submitted: t.Submitted, Stopped: t.Stopped,
			}, t.MovedTo, true
		},
		NotRecordingCause: watcher.NotRecordingCause,
		Tmux:              tm.Run,
		Pending:           watcher.ChatPending,
		Submitted:         watcher.Submitted,
		Working:           watcher.Working,
	}
	chatService := chat.New(chatOpts)
	gateTell = func(g aw.Gate) string {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, res := chatService.Deliver(ctx, g.Pane, g.AnswerText())
		if code == 200 {
			return "ok"
		}
		if e, ok := res["error"].(string); ok {
			return e
		}
		return fmt.Sprintf("HTTP %d", code)
	}

	ad := orch.NewAdopter(orch.AdoptOptions{
		Tmux: tm.Run, Agents: watcher.PaneAgents, State: watcher.PaneState, Source: watcher.PaneSource,
		LastTurn: chatService.LastTurn, Deliver: chatService.Deliver, DeliverOver: chatService.DeliverOver,
		LastAsked: chatService.LastAsked, Submitted: watcher.Submitted,
	})
	go ad.RunMail(ctx)
	ad.StartBriefs(ctx)

	// The plan limits come from what the agents leave on this machine (the
	// status line, session files) and from asking Anthropic and OpenAI every
	// few minutes with the logins their CLIs keep here (usage/poll.go; off
	// with stui usage poll off).
	usageEnv := usage.DefaultEnv(home.Dir())
	usageEnv.ClaudeFeed = func() bool { return hooks.StatuslineOn(hooks.DefaultEnv()) }
	usageService := usage.NewService(usage.ServiceOptions{Env: usageEnv})
	go usageService.Run(ctx, time.Minute)
	go (&usage.Poller{Env: usageEnv, UserAgent: "stui/" + Version, Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}).Run(ctx)

	actionOpts := ghactions.ServiceOptions{PaneCwd: m.PaneCwd}
	actionOpts.Tree = func(ctx context.Context, pane string) []ghactions.Member {
		var out []ghactions.Member
		for _, w := range orch.Subtree(pane, m.PaneKey(ctx, pane)) {
			out = append(out, ghactions.Member{Name: w.Name, Pane: w.Pane})
		}
		return out
	}
	actionOpts.Open = func(ctx context.Context, o ghactions.OpenWindow) (ghactions.Opened, error) {
		session, index, err := m.NamedWindow(ctx, o.Pane, o.Name, o.Key, o.Dir, o.Argv)
		if err != nil {
			return ghactions.Opened{}, err
		}
		return ghactions.Opened{Session: session, Window: index, Name: o.Name}, nil
	}

	var tb [16]byte
	rand.Read(tb[:])
	s := &server{m: m, tm: tm, muxVer: mux.Version(context.Background(), m), watcher: watcher, chat: chatService, orch: ad, usage: usageService,
		actions: ghactions.NewService(actionOpts), hooks: hookState, token: hex.EncodeToString(tb[:]), shutdown: stop}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	srv := &http.Server{Handler: s}
	go srv.Serve(ln)
	rec := &state.Agent{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: s.token,
		Session: currentSession(ctx, m), StartedAt: time.Now().UnixMilli(), Version: Version}
	if err := state.Write(rec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logf("stui %s serving on 127.0.0.1:%d (tmux %s)", Version, rec.Port, socket)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	select {
	case <-sig:
	case <-ctx.Done():
	}
	if r := state.Read(); r != nil && r.PID == os.Getpid() {
		state.Clear()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	return 0
}

// currentSession is the session stui was started from, else the first.
func currentSession(ctx context.Context, m *mux.Tmux) string {
	if p := os.Getenv("TMUX_PANE"); p != "" && os.Getenv("TMUX") != "" {
		if loc, ok := m.LocatePane(ctx, p); ok {
			return loc.Session
		}
	}
	if l := m.ListSessions(ctx); len(l) > 0 {
		return l[0].Name
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var sessionDelRE = regexp.MustCompile(`^/api/sessions/([^/]+)$`)

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only this machine's stui holds the token; a page in a browser does not.
	if r.Header.Get(state.TokenHeader) != s.token {
		writeJSON(w, 401, map[string]any{"error": "no token"})
		return
	}
	r = state.WithToken(r)
	path := r.URL.Path
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch {
	case path == "/api/version":
		writeJSON(w, 200, map[string]any{"version": Version, "mux": "tmux", "muxVersion": s.muxVer, "muxSocket": s.m.ServerSocket(ctx),
			"os":    runtime.GOOS + "/" + runtime.GOARCH,
			"hooks": s.hooks.States(), "hookSets": s.hooks.Sets(), "agentWatch": 1, "adopt": 1, "chat": 1, "usage": 1, "actions": 1})
		return
	case path == "/api/stop" && r.Method == "POST":
		writeJSON(w, 200, map[string]any{"ok": true})
		go s.shutdown()
		return
	case strings.HasPrefix(path, "/api/") && s.watcher.ServeHTTP(w, r):
		return
	case strings.HasPrefix(path, "/api/chat") && s.chat.ServeHTTP(w, r):
		return
	case strings.HasPrefix(path, "/api/orch/") && s.orch.ServeHTTP(w, r):
		return
	case (path == "/api/usage" || path == "/api/usage/guard") && s.usage.ServeHTTP(w, r):
		return
	case strings.HasPrefix(path, "/api/actions") && s.actions.ServeHTTP(w, r):
		return
	}
	q := r.URL.Query()
	agentOf := func() func(string) string {
		agents := s.watcher.PaneAgents()
		return func(p string) string { return agents[p] }
	}
	switch {
	case path == "/api/sessions" && r.Method == "GET":
		list := s.m.ListSessions(ctx)
		mux.AgentSessionIcons(list, agentOf())
		mux.AgentSessionCounts(list, s.m.PanesBySession(ctx), agentOf())
		writeJSON(w, 200, list)
	case path == "/api/sessions" && r.Method == "POST":
		var body struct {
			Name string `json:"name"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body)
		if !mux.ValidName(body.Name) {
			writeJSON(w, 400, map[string]any{"error": "name must match [A-Za-z0-9_-]{1,64}"})
			return
		}
		if _, err := s.tm.Run(ctx, "new-session", "-d", "-s", body.Name); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "name": body.Name})
	case sessionDelRE.MatchString(path) && r.Method == "DELETE":
		name, _ := url.PathUnescape(sessionDelRE.FindStringSubmatch(path)[1])
		if !mux.ValidName(name) {
			writeJSON(w, 400, map[string]any{"error": "bad name"})
			return
		}
		s.m.KillSession(ctx, name)
		writeJSON(w, 200, map[string]any{"ok": true})
	case path == "/api/windows" && r.Method == "GET":
		session := q.Get("session")
		if !mux.ValidName(session) {
			writeJSON(w, 400, map[string]any{"error": "bad session"})
			return
		}
		list := s.m.ListWindows(ctx, session, "")
		mux.AgentIcons(list, agentOf())
		writeJSON(w, 200, list)
	case path == "/api/windows" && r.Method == "POST":
		var body struct {
			Session string `json:"session"`
			Agent   string `json:"agent"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body)
		if !mux.ValidName(body.Session) {
			writeJSON(w, 400, map[string]any{"error": "bad session"})
			return
		}
		args := []string{"new-window", "-t", body.Session + ":"}
		// An agent is started the way a person would: typed into the new shell.
		if a := strings.TrimSpace(body.Agent); a != "" {
			if !agentName.MatchString(a) {
				writeJSON(w, 400, map[string]any{"error": "bad agent"})
				return
			}
			args = append(args, "-n", a, "-P", "-F", "#{pane_id}")
		}
		out, err := s.tm.Run(ctx, args...)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		if a := strings.TrimSpace(body.Agent); a != "" {
			s.tm.Run(ctx, "send-keys", "-t", strings.TrimSpace(out), "-l", a)
			s.tm.Run(ctx, "send-keys", "-t", strings.TrimSpace(out), "Enter")
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case path == "/api/windows" && r.Method == "DELETE":
		session, id := q.Get("session"), q.Get("window")
		if !mux.ValidName(session) || !mux.ValidWindowID(id) {
			writeJSON(w, 400, map[string]any{"error": "bad window"})
			return
		}
		if err := s.m.KillWindow(ctx, session, id); err != nil {
			writeJSON(w, 409, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case path == "/api/panes" && r.Method == "GET":
		session := q.Get("session")
		if !mux.ValidName(session) {
			writeJSON(w, 400, map[string]any{"error": "bad session"})
			return
		}
		list, ok := s.m.ListPanes(ctx, session)
		if !ok {
			list = []mux.PaneRow{}
		}
		states := s.watcher.PaneStates()
		mux.PaneAgents(list, agentOf(), func(id string) string { return states[id].State })
		writeJSON(w, 200, list)
	case path == "/api/panes/history" && r.Method == "GET":
		lines := 2000
		if n, err := strconv.Atoi(q.Get("lines")); err == nil && n > 0 {
			lines = min(n, mux.MaxHistoryLines)
		}
		h, ok := s.m.PaneHistory(ctx, q.Get("session"), q.Get("pane"), lines)
		if !ok {
			writeJSON(w, 404, map[string]any{"error": "no such pane"})
			return
		}
		writeJSON(w, 200, h)
	case path == "/api/action" && r.Method == "POST":
		var body struct {
			Session, Action, Target string
		}
		json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body)
		if !mux.ValidName(body.Session) || body.Action != string(mux.SelectPane) || !mux.ValidPaneTarget(body.Target) {
			writeJSON(w, 400, map[string]any{"error": "only select-pane with a session and a pane"})
			return
		}
		if _, err := s.m.RunPane(ctx, body.Session, mux.SelectPane, body.Target, ""); err != nil {
			code := 400
			if errors.Is(err, mux.ErrNoPane) {
				code = 409
			}
			writeJSON(w, code, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeJSON(w, 404, map[string]any{"error": "not found"})
	}
}

var agentName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// hookCache says which agents have stui hooks installed, and how fully,
// read from their files at most every 30 s.
type hookCache struct {
	mu     sync.Mutex
	at     time.Time
	have   map[string]bool
	states map[string]string
	sets   map[string]hooks.SetInfo
}

func (c *hookCache) readLocked() {
	if c.have != nil && time.Since(c.at) < 30*time.Second {
		return
	}
	e := hooks.DefaultEnv()
	installed, _ := hooks.ClaudeStatus(e)
	c.have = map[string]bool{"claude": len(installed) > 0, "codex": hooks.CodexStatus(e)}
	c.states = hooks.States(e)
	c.sets = hooks.Sets(e)
	c.at = time.Now()
}

func (c *hookCache) Installed() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLocked()
	out := map[string]bool{}
	for k, v := range c.have {
		out[k] = v
	}
	return out
}

func (c *hookCache) Sets() map[string]hooks.SetInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLocked()
	out := map[string]hooks.SetInfo{}
	for k, v := range c.sets {
		out[k] = v
	}
	return out
}

func (c *hookCache) States() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readLocked()
	out := map[string]string{}
	for k, v := range c.states {
		out[k] = v
	}
	return out
}

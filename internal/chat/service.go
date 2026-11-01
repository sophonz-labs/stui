package chat

// The routes:
//
//	GET /api/chat?pane=%3&tail=40[&before=<offset>]   the last messages
//	GET /api/chat/stream?pane=%3[&tail=40]             SSE: snapshot, then append,
//	                                                   replace, turn, reset
//	GET /api/chat/block?pane=%3&id=<message>[&i=<n>]   a message's blocks whole
//	GET /api/chat/notice                               Orca's license (MIT)
//	POST /api/chat/send?pane=%3                        type a message (send.go)
//	POST /api/chat/stop?pane=%3                        stop the turn (send.go)
//
// A stream reads the file only while somebody is listening: one reader per
// pane, however many listen. It looks at the file every 50 ms, sends what
// was added once it has been still for 40 ms (or has kept growing for
// 250 ms), and once a second checks that it is still the pane's file and the
// same file. The batching numbers are Orca's transcript-watch-scheduler.ts
// (MIT, Copyright (c) 2026 Lovecast Inc.).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/stream"
)

// Timing, shortened by tests.
var (
	pollEvery      = 50 * time.Millisecond
	settleFor      = 40 * time.Millisecond
	drainWithin    = 250 * time.Millisecond
	reconcileEvery = time.Second
)

const (
	// Messages in one append or replace event.
	batch = 40
	// Events a listener may fall behind by before it is dropped (it comes
	// back to a new snapshot).
	backlog = 256
	// Message ids a stream remembers sending, to tell a record written again
	// (replace) from a new one.
	rememberIDs = 4096
	defaultTail = 40
)

// Unavailable reasons.
const (
	NoAgent          = "no-agent"
	UnsupportedAgent = "unsupported-agent"
	NoTranscript     = "no-transcript"
	Moved            = "moved"
	// The transcript is there but the agent stopped writing it: a whole
	// turn has gone by since its last write (a Claude Code that inherited
	// CLAUDE_CODE_CHILD_SESSION, for one). What it holds is shown; Since
	// says from when nothing more can be.
	NotRecording = "not-recording"
)

type Options struct {
	// What explains a pane's agent not writing its transcript, "" when
	// nothing stui can see does (NotRecording).
	NotRecordingCause func(pane string) string
	// What agent watch knows of a pane's conversation: false for a pane with
	// no agent. movedTo is the pane that has taken the conversation over.
	Source func(pane string) (s Source, movedTo string, ok bool)
	// Whether r may read conversations: the machine's owner. A read-only
	// viewer may not -- a transcript holds more than the screen it was
	// given. nil: everybody past the gate.
	IsOwner func(*http.Request) bool

	// Typing for the chat (send.go). nil: stui does not type, and
	// /api/version says chat 1.
	Tmux func(ctx context.Context, args ...string) (string, error)
	// Whether agent watch has a question or a permission prompt open for
	// the pane, or the pane waits on one.
	Pending func(pane string) bool
	// When the pane's main agent last took a prompt (its UserPromptSubmit
	// hook); zero for never.
	Submitted func(pane string) time.Time
	// Whether the pane's agent is in a turn.
	Working func(pane string) bool
}

type Service struct {
	o    Options
	mu   sync.Mutex
	hubs map[string]*hub
	// Panes with a write sequence going (send.go).
	writing map[string]bool
}

// Level is what /api/version says of the chat: 2 when stui types for it
// (POST /api/chat/send and /api/chat/stop), else 1.
func (s *Service) Level() int {
	if s.sends() {
		return 2
	}
	return 1
}

func New(o Options) *Service {
	return &Service{o: o, hubs: map[string]*hub{}}
}

var paneRE = regexp.MustCompile(`^(?:[A-Za-z0-9_-]{1,64}:)?%[0-9]{1,9}$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// ServeHTTP answers the routes above and reports whether r was one. The
// caller has already let r through the gate.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path == "/api/chat/notice" && r.Method == "GET" {
		// The license of what this package ports from Orca.
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		io.WriteString(w, Notice)
		return true
	}
	writes := path == "/api/chat/send" || path == "/api/chat/stop"
	if writes && !s.sends() {
		return false
	}
	if path != "/api/chat" && path != "/api/chat/stream" && path != "/api/chat/block" && !writes {
		return false
	}
	if (writes && r.Method != "POST") || (!writes && r.Method != "GET") {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	if s.o.IsOwner != nil && !s.o.IsOwner(r) {
		writeJSON(w, 403, map[string]any{"error": "read-only"})
		return true
	}
	q := r.URL.Query()
	pane := q.Get("pane")
	if !paneRE.MatchString(pane) {
		writeJSON(w, 400, map[string]any{"error": "bad pane"})
		return true
	}
	// A subagent's own conversation, read-only (Claude: its file beside the
	// main one).
	sub := q.Get("subagent")
	if sub != "" && (writes || !safeID.MatchString(sub)) {
		writeJSON(w, 400, map[string]any{"error": "bad subagent"})
		return true
	}
	at := ref{pane: pane, sub: sub}
	switch path {
	case "/api/chat/send":
		s.send(w, r, pane)
		return true
	case "/api/chat/stop":
		s.stop(w, r, pane)
		return true
	}
	tail := defaultTail
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSON(w, 400, map[string]any{"error": "bad tail"})
			return true
		}
		tail = min(n, MaxTail)
	}
	switch path {
	case "/api/chat":
		var before int64
		if v := q.Get("before"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				writeJSON(w, 400, map[string]any{"error": "bad before"})
				return true
			}
			before = n
		}
		s.snapshot(w, at, tail, before)
	case "/api/chat/stream":
		s.stream(w, r, at, tail)
	case "/api/chat/block":
		s.block(w, at, q.Get("id"), q.Get("i"))
	}
	return true
}

// ref is a conversation: a pane's, or one of its agent's subagents'.
type ref struct {
	pane, sub string
}

func (r ref) key() string {
	return r.pane + "/" + r.sub
}

// resolved is where a pane's conversation is, or why there is none.
type resolved struct {
	agent, session, path string
	unavailable, movedTo string
	// The source's turn times, for NotRecording.
	submitted, stopped time.Time
}

func (r resolved) key() string {
	return r.agent + "\x00" + r.session + "\x00" + r.path + "\x00" + r.unavailable + "\x00" + r.movedTo
}

func (s *Service) resolve(pane string) resolved {
	if s.o.Source == nil {
		return resolved{unavailable: NoAgent}
	}
	src, moved, ok := s.o.Source(pane)
	if !ok || src.Agent == "" {
		return resolved{unavailable: NoAgent}
	}
	r := resolved{agent: src.Agent, session: src.SessionID, submitted: src.Submitted, stopped: src.Stopped}
	switch {
	case !Readable(src.Agent):
		r.unavailable = UnsupportedAgent
	case moved != "":
		r.unavailable, r.movedTo = Moved, moved
	default:
		if r.path = Locate(src); r.path == "" {
			r.unavailable = NoTranscript
		}
	}
	return r
}

// resolveRef is resolve for a pane's own conversation, and for a subagent
// the file of its own beside it (Claude only).
func (s *Service) resolveRef(at ref) resolved {
	r := s.resolve(at.pane)
	if at.sub == "" || r.path == "" {
		return r
	}
	if r.agent != "claude" {
		return resolved{agent: r.agent, session: r.session, unavailable: UnsupportedAgent}
	}
	r.agent, r.path = claudeSubagent, LocateSubagent(r.path, at.sub)
	if r.path == "" {
		r.unavailable = NoTranscript
	}
	return r
}

// Snapshot is GET /api/chat's answer and the stream's first event.
type Snapshot struct {
	Pane      string `json:"pane"`
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	// The subagent whose own conversation this is.
	Subagent    string    `json:"subagent,omitempty"`
	Unavailable string    `json:"unavailable,omitempty"`
	MovedTo     string    `json:"movedTo,omitempty"`
	Messages    []Message `json:"messages"`
	HasMore     bool      `json:"hasMore"`
	Before      int64     `json:"before,omitempty"`
	Turn        *Turn     `json:"turn"`
	// With "not-recording": the transcript's last write (ms), and what stui
	// saw that explains it, when anything ("CLAUDE_CODE_CHILD_SESSION").
	Since int64  `json:"since,omitempty"`
	Cause string `json:"cause,omitempty"`
}

func (s *Service) window(at ref, res resolved, end, before int64, tail int) (Snapshot, decoder, error) {
	agent := res.agent
	if agent == claudeSubagent {
		agent = "claude"
	}
	snap := Snapshot{Pane: at.pane, Agent: agent, SessionID: res.session, Subagent: at.sub, Unavailable: res.unavailable, MovedTo: res.movedTo, Messages: []Message{}}
	if res.path == "" {
		return snap, nil, nil
	}
	win, dec, err := readTail(res.path, res.agent, end, before, tail)
	if err != nil {
		return snap, nil, err
	}
	for _, m := range win.Messages {
		snap.Messages = append(snap.Messages, Clip(m, Wire))
	}
	snap.HasMore, snap.Before, snap.Turn = win.HasMore, win.Before, win.Turn
	if at.sub == "" {
		if since, stale := notRecording(res.path, res.submitted, res.stopped, time.Now()); stale {
			snap.Unavailable, snap.Since = NotRecording, since.UnixMilli()
			if s.o.NotRecordingCause != nil {
				snap.Cause = s.o.NotRecordingCause(at.pane)
			}
		}
	}
	return snap, dec, nil
}

// notRecording is whether the transcript at path has missed a whole turn:
// the agent took a prompt and ended that turn, both after the file's last
// write and the end a little while ago (a write can trail the hook). It
// says when the file was last written.
func notRecording(path string, submitted, stopped, now time.Time) (time.Time, bool) {
	if path == "" || submitted.IsZero() || stopped.Before(submitted) || now.Sub(stopped) < 5*time.Second {
		return time.Time{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	m := st.ModTime()
	return m, m.Before(submitted.Add(-2 * time.Second))
}

func (s *Service) snapshot(w http.ResponseWriter, at ref, tail int, before int64) {
	snap, _, err := s.window(at, s.resolveRef(at), -1, before, tail)
	switch {
	case err == ErrBadOffset:
		writeJSON(w, 400, map[string]any{"error": "bad before"})
	case err != nil:
		snap.Unavailable = NoTranscript
		writeJSON(w, 200, snap)
	default:
		writeJSON(w, 200, snap)
	}
}

// block answers a message's blocks whole (up to Full).
func (s *Service) block(w http.ResponseWriter, at ref, id, idx string) {
	res := s.resolveRef(at)
	if res.path == "" {
		writeJSON(w, 404, map[string]any{"error": res.unavailable})
		return
	}
	if id == "" || len(id) > 200 {
		writeJSON(w, 400, map[string]any{"error": "bad id"})
		return
	}
	m, ok := findMessage(res.path, res.agent, id)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "no such message"})
		return
	}
	m = Clip(m, Full)
	if idx == "" {
		writeJSON(w, 200, map[string]any{"id": m.ID, "blocks": m.Blocks})
		return
	}
	i, err := strconv.Atoi(idx)
	if err != nil || i < 0 || i >= len(m.Blocks) {
		writeJSON(w, 404, map[string]any{"error": "no such block"})
		return
	}
	writeJSON(w, 200, map[string]any{"id": m.ID, "i": i, "block": m.Blocks[i]})
}

// findMessage reads the message id from the transcript: one that has no id
// of its own at the offset it is named by, any other by looking for its id,
// from the end.
func findMessage(path, agent, id string) (Message, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return Message{}, false
	}
	defer fh.Close()
	dec := newDecoder(agent)
	if dec == nil {
		// An agent whose conversation stui does not read (opencode).
		return Message{}, false
	}
	pick := func(l Line) (Message, bool) {
		msgs, _ := dec.decode(l.Data, l.Off)
		for _, m := range msgs {
			if m.ID == id {
				return m, true
			}
		}
		return Message{}, false
	}
	if off, err := strconv.ParseInt(strings.TrimPrefix(id, "o"), 10, 64); err == nil && strings.HasPrefix(id, "o") {
		if off > 0 {
			var b [1]byte
			if _, err := fh.ReadAt(b[:], off-1); err != nil || b[0] != '\n' {
				return Message{}, false
			}
		}
		if _, err := fh.Seek(off, io.SeekStart); err != nil {
			return Message{}, false
		}
		r := bufio.NewReaderSize(fh, 64<<10)
		var line []byte
		for len(line) <= MaxRecord {
			part, isPrefix, err := r.ReadLine()
			line = append(line, part...)
			if err != nil || !isPrefix {
				break
			}
		}
		if len(line) > MaxRecord {
			return Message{}, false
		}
		return pick(Line{Off: off, Data: line})
	}
	st, err := fh.Stat()
	if err != nil {
		return Message{}, false
	}
	needle := []byte(`"` + id + `"`)
	var found Message
	ok := false
	readBack(fh, lineEnd(fh, st.Size()), func(l Line) bool {
		if !bytes.Contains(l.Data, needle) {
			return true
		}
		found, ok = pick(l)
		return !ok
	})
	return found, ok
}

// ---- the stream

type event struct {
	name string
	seq  int64
	data any
}

type listener struct {
	ch   chan event
	tail int
}

// hub reads one conversation's transcript for everyone listening to it.
type hub struct {
	s  *Service
	at ref

	mu   sync.Mutex
	subs map[*listener]bool
	res  resolved
	fol  *Follower
	dec  decoder
	seq  int64
	// Ids sent, oldest first, to tell replace from append.
	sentOrder []string
	sent      map[string]bool
	// The size at the last poll, and when it last changed while there was
	// something to read.
	lastSize           int64
	firstGrow, lastGrw time.Time
	pending            bool
	lastReconcile      time.Time
}

// Listen adds a listener to the stream of at: its first event is the
// snapshot. cancel ends it.
func (s *Service) Listen(at ref, tail int) (<-chan event, func()) {
	s.mu.Lock()
	h := s.hubs[at.key()]
	fresh := h == nil
	if fresh {
		h = &hub{s: s, at: at, subs: map[*listener]bool{}, sent: map[string]bool{}}
		s.hubs[at.key()] = h
	}
	l := &listener{ch: make(chan event, backlog), tail: tail}
	h.mu.Lock()
	if fresh {
		h.openLocked()
	}
	h.subs[l] = true
	h.snapshotLocked(l)
	h.mu.Unlock()
	s.mu.Unlock()
	if fresh {
		go h.run()
	}
	cancel := func() {
		s.mu.Lock()
		h.mu.Lock()
		if h.subs[l] {
			delete(h.subs, l)
			close(l.ch)
		}
		if len(h.subs) == 0 && s.hubs[at.key()] == h {
			delete(s.hubs, at.key())
		}
		h.mu.Unlock()
		s.mu.Unlock()
	}
	return l.ch, cancel
}

// openLocked finds the pane's file and starts reading at its end.
func (h *hub) openLocked() {
	h.res = h.s.resolveRef(h.at)
	h.fol, h.dec, h.pending = nil, nil, false
	clear(h.sent)
	h.sentOrder = h.sentOrder[:0]
	h.lastReconcile = time.Now()
	if h.res.path == "" {
		return
	}
	win, dec, err := readTail(h.res.path, h.res.agent, -1, 0, 1)
	if err != nil {
		h.res.unavailable, h.res.path = NoTranscript, ""
		return
	}
	fol, err := NewFollower(h.res.path, win.End)
	if err != nil {
		h.res.unavailable, h.res.path = NoTranscript, ""
		return
	}
	h.fol, h.dec, h.lastSize = fol, dec, fol.size
}

// snapshotLocked sends l the conversation up to where the hub has read.
func (h *hub) snapshotLocked(l *listener) {
	end := int64(-1)
	if h.fol != nil {
		end = h.fol.Offset()
	}
	snap, _, err := h.s.window(h.at, h.res, end, 0, l.tail)
	if err != nil {
		snap.Unavailable = NoTranscript
	}
	for _, m := range snap.Messages {
		h.noteSentLocked(m.ID)
	}
	h.sendLocked(l, "snapshot", snap)
}

func (h *hub) noteSentLocked(id string) bool {
	if h.sent[id] {
		return true
	}
	h.sent[id] = true
	h.sentOrder = append(h.sentOrder, id)
	if len(h.sentOrder) > rememberIDs {
		delete(h.sent, h.sentOrder[0])
		h.sentOrder = h.sentOrder[1:]
	}
	return false
}

func (h *hub) sendLocked(l *listener, name string, data any) {
	h.seq++
	select {
	case l.ch <- event{name: name, seq: h.seq, data: data}:
	default:
		// Too far behind: dropped. It comes back to a new snapshot.
		delete(h.subs, l)
		close(l.ch)
	}
}

func (h *hub) broadcastLocked(name string, data any) {
	for l := range h.subs {
		h.sendLocked(l, name, data)
	}
}

// resetLocked starts over: everyone is told why, and gets a new snapshot.
func (h *hub) resetLocked(reason string) {
	h.broadcastLocked("reset", map[string]any{"reason": reason})
	h.openLocked()
	for l := range h.subs {
		h.snapshotLocked(l)
	}
}

func (h *hub) run() {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for range t.C {
		h.s.mu.Lock()
		h.mu.Lock()
		if len(h.subs) == 0 {
			if h.s.hubs[h.at.key()] == h {
				delete(h.s.hubs, h.at.key())
			}
			h.mu.Unlock()
			h.s.mu.Unlock()
			return
		}
		h.s.mu.Unlock()
		h.tickLocked(time.Now())
		h.mu.Unlock()
	}
}

func (h *hub) tickLocked(now time.Time) {
	if now.Sub(h.lastReconcile) >= reconcileEvery {
		h.lastReconcile = now
		res := h.s.resolveRef(h.at)
		if res.key() != h.res.key() {
			reason := "switched"
			if res.path == "" {
				reason = res.unavailable
			}
			if h.res.path == "" {
				// Nothing was shown: the snapshot alone says it.
				h.openLocked()
				for l := range h.subs {
					h.snapshotLocked(l)
				}
				return
			}
			h.resetLocked(reason)
			return
		}
		if h.fol != nil {
			switch h.fol.Verify() {
			case Replaced:
				h.resetLocked("rewritten")
				return
			case Gone:
				h.resetLocked(NoTranscript)
				return
			}
		}
	}
	if h.fol == nil {
		return
	}
	st, err := os.Stat(h.res.path)
	if err != nil {
		return
	}
	if size := st.Size(); size != h.lastSize {
		if size < h.lastSize || size < h.fol.Offset() {
			h.resetLocked("rewritten")
			return
		}
		if !h.pending {
			h.pending, h.firstGrow = true, now
		}
		h.lastSize, h.lastGrw = size, now
	}
	if h.pending && (now.Sub(h.lastGrw) >= settleFor || now.Sub(h.firstGrow) >= drainWithin) {
		h.pending = false
		h.drainLocked()
	}
}

// drainLocked reads what was added and sends it.
func (h *hub) drainLocked() {
	lines, err := h.fol.Read()
	if err == ErrReplaced {
		h.resetLocked("rewritten")
		return
	}
	var appends, replaces []Message
	flush := func() {
		for len(replaces) > 0 {
			n := min(len(replaces), batch)
			h.broadcastLocked("replace", map[string]any{"messages": replaces[:n]})
			replaces = replaces[n:]
		}
		for len(appends) > 0 {
			n := min(len(appends), batch)
			h.broadcastLocked("append", map[string]any{"messages": appends[:n]})
			appends = appends[n:]
		}
	}
	for _, l := range lines {
		msgs, t := h.dec.decode(l.Data, l.Off)
		for _, m := range msgs {
			m = Clip(m, Wire)
			if h.noteSentLocked(m.ID) {
				if len(appends) > 0 {
					flush()
				}
				replaces = append(replaces, m)
			} else {
				if len(replaces) > 0 {
					flush()
				}
				appends = append(appends, m)
			}
		}
		if t != nil {
			flush()
			h.broadcastLocked("turn", *t)
		}
	}
	flush()
}

// stream is the chat feed as SSE or, with an
// upgrade, a websocket (internal/stream says why).
func (s *Service) stream(w http.ResponseWriter, r *http.Request, at ref, tail int) {
	stream.Serve(w, r, func(ctx context.Context, send stream.Send) {
		events, cancel := s.Listen(at, tail)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok || !send(ev.name, ev.seq, ev.data) {
					return
				}
			}
		}
	})
}

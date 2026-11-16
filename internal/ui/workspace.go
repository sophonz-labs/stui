package ui

// The workspace (stui --workspace, or ui.workspace: true in config.yaml):
// once stui attaches, it keeps the terminal. The frame is tmui's
// pkg/workspace: a tmux client of stui's server in the middle (its own
// client, $TMUX cleared), this machine's sessions, windows and agents on
// the left, the agents graph, history and documents on the right, the menu
// bar and the mouse. Only this machine: no other machines, no account.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/cmdicon"
	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/vault"
	"github.com/sophonz-labs/tmui/pkg/workspace"
)

// wsHost is stui to the frame.
type wsHost struct{ a *app }

// runWorkspace attaches to session in the workspace and returns once that
// ends. false: the workspace cannot run here (Windows), nothing was done.
func (a *app) runWorkspace(session string) bool {
	f := workspace.New(&wsHost{a: a}, workspace.Options{
		Name:      "stui",
		About:     "stui --workspace: tmux in the middle, this machine on the left; stui --help says more",
		Out:       a.scr.w,
		Size:      a.scr.size,
		Later:     a.later,
		Prefs:     filepath.Join(home.Dir(), "ui.json"),
		PrefixEnv: "STUI_PREFIX",
		Icons:     a.icons,
		NoDocs:    "(stui doc <file>)",
	}, wsSnap(a.snap), session, nil)
	// New snapshots reach the frame on its own goroutine, through later.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-a.snaps:
				a.later <- func() { a.snap = s; f.Set(wsSnap(s)) }
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)
	a.scr.pause()
	defer a.scr.resume()
	why, ok := f.Run()
	if !ok {
		return false
	}
	a.say(why, false)
	return true
}

// wsSnap is the frame's part of a snapshot: stui's own agents (not st's,
// which are on another server), and the limits and memory for the bottom
// row.
func wsSnap(s *snap) *workspace.Snap {
	out := &workspace.Snap{Hostname: hostname, Mux: "tmux", MuxSocket: s.version.MuxSocket, Windows: map[string][]workspace.Window{}}
	for _, x := range s.sessions {
		out.Sessions = append(out.Sessions, workspace.Session(x))
	}
	for k, ws := range s.windows {
		for _, w := range ws {
			out.Windows[k] = append(out.Windows[k], workspace.Window(w))
		}
	}
	for _, x := range s.agents.Sessions {
		out.Agents.Sessions = append(out.Agents.Sessions, workspace.AgentSession(x))
	}
	for _, p := range s.agents.Panes {
		if p.st {
			continue
		}
		wp := workspace.AgentPane{ID: p.ID, Session: p.Session, Window: p.Window, WindowName: p.WindowName, Pane: p.Pane,
			Agent: p.Agent, Command: p.Command, State: p.State, Source: p.Source, Since: p.Since, Turn: p.Turn, Title: p.Title}
		wp.Subagents.Running = p.Subagents.Running
		if p.Orch != nil {
			o := workspace.OrchInfo(*p.Orch)
			wp.Orch = &o
		}
		out.Agents.Panes = append(out.Agents.Panes, wp)
	}
	var parts []string
	for _, ag := range s.usage.Agents {
		for _, uw := range ag.Windows {
			parts = append(parts, fmt.Sprintf("%s %s %.0f%%", ag.Agent, limitName(uw), uw.UsedPercent))
		}
	}
	if s.metrics.Machine.MemTotal > 0 {
		parts = append(parts, "mem "+gib(s.latest().MemUsed)+"/"+gib(s.metrics.Machine.MemTotal))
	}
	out.Status = strings.Join(parts, " · ")
	return out
}

// hostname names the machine row: this machine's name without ".local".
var hostname = func() string {
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}()

// stuiIcon is an agent kind's icon from the command icons (internal/cmdicon).
func stuiIcon(agent string) (string, []byte, string) {
	m := cmdicon.Resolve(agent, "", nil)
	svg, _ := cmdicon.SVG(m.Icon)
	return m.Icon, svg, cmdicon.Color(agent)
}

// Attach is a client of stui's server on session, at a window when more
// says --window n.
func (h *wsHost) Attach(target string, more []string) []string {
	argv := []string{"tmux"}
	if s := h.a.snap.version.MuxSocket; s != "" {
		argv = append(argv, "-S", s)
	}
	t := "=" + target
	if len(more) == 2 && more[0] == "--window" {
		t += ":" + more[1]
	}
	return append(argv, "attach-session", "-t", t)
}
func (h *wsHost) Get(path string, out any) error { return h.a.c.get(path, out) }
func (h *wsHost) Do(method, path string, body, out any) error {
	return h.a.c.do(method, path, body, out)
}
func (h *wsHost) Call(method, path string, body any, done string) {
	h.a.call(method, path, body, done)
}
func (h *wsHost) Say(text string, bad bool) { h.a.say(text, bad) }
func (h *wsHost) Refresh()                  { h.a.refreshNow() }
func (h *wsHost) Rows(*workspace.Frame) []workspace.Row {
	return nil // this machine only
}

// Flash is what was said in the last five seconds.
func (h *wsHost) Flash() (string, bool) {
	if h.a.flash != "" && time.Since(h.a.flashAt) > 5*time.Second {
		h.a.flash = ""
	}
	return h.a.flash, h.a.bad
}

// Command is ":": a view or quitting goes back to the list to run there; a
// stui command runs here, its last line on the bottom row.
func (h *wsHost) Command(text string) bool {
	words := splitWords(strings.TrimSpace(text))
	if len(words) > 0 && words[0] == "stui" {
		words = words[1:]
	}
	if len(words) == 0 || words[0] == "ui" {
		return false
	}
	w := words[0]
	if w == "q" || w == "quit" || w == "help" || w == "?" || viewNames[w] != nil || w == "windows" || w == "panes" {
		h.a.later <- func() { h.a.runCommand(text) }
		return true
	}
	h.a.runQuiet("done", words...)
	return false
}

// Docs is the vault by folder (session ▸ window), read from disk.
func (h *wsHost) Docs() (*workspace.DocNode, error) {
	ds, err := vault.List()
	if err != nil {
		return nil, err
	}
	return docTree(ds), nil
}

// docTree is ds as folders: each path's folders a node, its file a document.
func docTree(ds []vault.Doc) *workspace.DocNode {
	root := &workspace.DocNode{}
	for _, d := range ds {
		n := root
		if dir := path.Dir(d.Path); dir != "." {
			for _, part := range strings.Split(dir, "/") {
				var next *workspace.DocNode
				for _, c := range n.Children {
					if c.Name == part {
						next = c
					}
				}
				if next == nil {
					next = &workspace.DocNode{Name: part}
					n.Children = append(n.Children, next)
				}
				next.Count++
				n = next
			}
		}
		n.Docs = append(n.Docs, workspace.DocItem{ID: d.Path, Title: d.Title})
	}
	root.Count = len(ds)
	return root
}

func (h *wsHost) Doc(id string) (workspace.Doc, error) {
	b, err := vault.Read(id)
	if err != nil {
		return workspace.Doc{}, err
	}
	folder := path.Dir(id)
	if folder == "." {
		folder = ""
	}
	return workspace.Doc{Title: strings.TrimSuffix(path.Base(id), path.Ext(id)), Content: string(b), Folder: folder}, nil
}

// File is a picture a document shows, by its path in the vault.
func (h *wsHost) File(p string) ([]byte, error) { return vault.Read(p) }

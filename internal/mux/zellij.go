package mux

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/sophonz-labs/stui/internal/home"
)

// zellij greets a new session with an "About Zellij" popup that takes the
// keyboard. On a phone there is no way to dismiss it, so every keystroke is
// swallowed. This config turns it off.
//
//go:embed zellij.kdl
var zellijConfig []byte

// Zellij drives sessions through zellij's own action API rather than
// synthesised prefix keys, which an IME on the phone cannot mangle.
//
// A compiled stui carries its own zellij, and it has to be the same binary for
// these calls as for the shell ttyd starts, or they would be talking about
// different sessions.
type Zellij struct {
	bin    string
	config string
}

func NewZellij() *Zellij {
	bin := os.Getenv("STUI_ZELLIJ_BIN")
	if bin == "" {
		bin = "zellij"
	}
	cfg := os.Getenv("STUI_ZELLIJ_CONFIG")
	if cfg == "" {
		cfg = writtenConfig()
	}
	return &Zellij{bin: bin, config: cfg}
}

var configOnce sync.Once
var configPath string

// writtenConfig puts the embedded config on disk for a build that did not
// unpack one of its own.
func writtenConfig() string {
	configOnce.Do(func() {
		path := filepath.Join(home.Dir(), "zellij.kdl")
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.WriteFile(path, zellijConfig, 0o644) == nil {
			configPath = path
		}
	})
	return configPath
}

func (z *Zellij) ID() string    { return "zellij" }
func (z *Zellij) Label() string { return "zellij" }

// Actions is everything but zoom-pane, select-pane and unzoom: zellij cannot
// focus a pane by index, nor ask whether a tab is zoomed.
func (z *Zellij) Actions() []Action {
	var out []Action
	for _, a := range allActions {
		if a != ZoomPane && a != SelectPane && a != Unzoom {
			out = append(out, a)
		}
	}
	return out
}
func (z *Zellij) Persistent() bool { return true }

// IndependentClientSize is false: zellij has no session groups, so a second
// client joins the same session and shares its size.
func (z *Zellij) IndependentClientSize() bool { return false }
func (z *Zellij) Command() string             { return z.bin }
func (z *Zellij) Available() bool             { return installed(z.bin) }

func (z *Zellij) action(ctx context.Context, session string, args ...string) (string, bool) {
	return sh(ctx, z.bin, append([]string{"-s", session, "action"}, args...)...)
}

func (z *Zellij) ListSessions(ctx context.Context) []SessionInfo {
	// -s prints bare names; the default output carries colour and an age.
	out, ok := sh(ctx, z.bin, "list-sessions", "-s")
	if !ok {
		return []SessionInfo{}
	}
	names := lines(out)
	list := make([]SessionInfo, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		name = strings.TrimSpace(name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			list[i] = SessionInfo{
				Name:          name,
				Windows:       z.tabCount(ctx, name),
				Attached:      z.clientCount(ctx, name),
				CurrentWindow: z.currentTab(ctx, name),
			}
		}()
	}
	wg.Wait()
	return list
}

func (z *Zellij) tabCount(ctx context.Context, name string) int {
	out, ok := z.action(ctx, name, "query-tab-names")
	if !ok {
		return 0
	}
	return len(lines(out))
}

func (z *Zellij) clientCount(ctx context.Context, name string) int {
	out, ok := z.action(ctx, name, "list-clients")
	if !ok {
		return 0
	}
	return max(0, len(lines(out))-1) // the first line is a header
}

var tabName = regexp.MustCompile(`(?i)name:\s*(.+)`)

func (z *Zellij) currentTab(ctx context.Context, name string) string {
	out, ok := z.action(ctx, name, "current-tab-info")
	if !ok {
		return ""
	}
	if m := tabName.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
}

func (z *Zellij) HasSession(ctx context.Context, name string) bool {
	for _, s := range z.ListSessions(ctx) {
		if s.Name == name {
			return true
		}
	}
	return false
}

// CreateSession is a no-op by design: a zellij session comes into being when
// a client attaches, which AttachArgs' -c does.
func (z *Zellij) CreateSession(_ context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid session name: %s", name)
	}
	return nil
}

func (z *Zellij) KillSession(ctx context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid session name: %s", name)
	}
	sh(ctx, z.bin, "kill-session", name)
	sh(ctx, z.bin, "delete-session", name)
	return nil
}

// AttachArgs ignores client: every client shares one session and its size.
func (z *Zellij) AttachArgs(session, _ string) []string {
	args := []string{}
	if z.config != "" {
		args = append(args, "--config", z.config)
	}
	return append(args, "attach", "-c", session)
}

var tabRow = regexp.MustCompile(`^(\d+)\s+(\d+)\s+(.*)$`)

// parseTabs reads `list-tabs`: TAB_ID POSITION NAME under a header.
// goto-window takes a 1-based position, which is what index has to be.
// active is always false: zellij 0.45 does not report the focused tab to the
// CLI, and highlighting nothing beats guessing wrong.
func parseTabs(out string) []WindowInfo {
	list := []WindowInfo{}
	rows := strings.Split(out, "\n")
	if len(rows) > 0 {
		rows = rows[1:]
	}
	for _, line := range rows {
		m := tabRow.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		pos, _ := strconv.Atoi(m[2])
		// zellij reports no tab width; 0 means "do not try to fit to this".
		list = append(list, WindowInfo{Index: pos + 1, ID: m[1], Name: strings.TrimSpace(m[3])})
	}
	return list
}

func (z *Zellij) ListWindows(ctx context.Context, session, _ string) []WindowInfo {
	out, ok := z.action(ctx, session, "list-tabs")
	if !ok {
		return []WindowInfo{}
	}
	return parseTabs(out)
}

// scroll uses page and half-page actions for anything past a few lines:
// zellij's scroll actions move one line and take no count.
func (z *Zellij) scroll(ctx context.Context, session, dir string, n int) {
	for ; n >= 15; n -= 15 {
		z.action(ctx, session, "page-scroll-"+dir)
	}
	if n >= 7 {
		z.action(ctx, session, "half-page-scroll-"+dir)
		n -= 7
	}
	for ; n > 0; n-- {
		z.action(ctx, session, "scroll-"+dir)
	}
}

func (z *Zellij) Run(ctx context.Context, session string, action Action, arg, _ string) error {
	switch action {
	case ScrollEnd:
		z.action(ctx, session, "scroll-to-bottom")
		return nil
	case ScrollUp, ScrollDown:
		dir := "up"
		if action == ScrollDown {
			dir = "down"
		}
		z.scroll(ctx, session, dir, max(1, num(arg)))
		return nil
	}

	var args []string
	switch action {
	case NextWindow:
		args = []string{"go-to-next-tab"}
	case PrevWindow:
		args = []string{"go-to-previous-tab"}
	case NewWindow:
		args = []string{"new-tab"}
	case CloseWindow:
		args = []string{"close-tab"}
	case GotoWindow:
		args = []string{"go-to-tab", strconv.Itoa(num(arg))}
	case RenameWindow:
		args = []string{"rename-tab", arg}
	case NextPane:
		args = []string{"focus-next-pane"}
	case Zoom:
		args = []string{"toggle-fullscreen"}
	case SplitRight:
		args = []string{"new-pane", "-d", "right"}
	case SplitDown:
		args = []string{"new-pane", "-d", "down"}
	case ClosePane:
		args = []string{"close-pane"}
	case Detach:
		args = []string{"detach"}
	}
	if args != nil {
		// Said, not swallowed: see Tmux.Run.
		if _, ok := z.action(ctx, session, args...); !ok {
			return fmt.Errorf("zellij %s on %s failed", args[0], session)
		}
	}
	return nil
}

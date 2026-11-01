package mux

import "context"

// None runs the command directly under ttyd.
//
// The shell lives exactly as long as the websocket: ttyd sends SIGHUP to its
// child when a client disconnects, and a reconnect gets a fresh process. On
// a phone that means screen lock or a network change ends the session, so
// this mode says so rather than pretending otherwise.
type None struct{ command, session string }

func NewNone(command string) *None { return &None{command: command} }

// DefaultSession is what the one shell is called until the agent says
// otherwise: the default of stui's --session.
const DefaultSession = "default"

// SetSession names the one shell. Listed under the shell's path instead, it
// would have a name no client accepts as a session name.
func (n *None) SetSession(name string) { n.session = name }

// Session is the name the one shell is listed under.
func (n *None) Session() string {
	if n.session == "" {
		return DefaultSession
	}
	return n.session
}

func (n *None) ID() string                  { return "none" }
func (n *None) Label() string               { return n.command }
func (n *None) Actions() []Action           { return []Action{} }
func (n *None) Persistent() bool            { return false }
func (n *None) IndependentClientSize() bool { return true } // each connection is its own pty
func (n *None) Command() string             { return n.command }
func (n *None) Available() bool             { return installed(n.command) }

func (n *None) ListSessions(context.Context) []SessionInfo {
	// The command goes where the list shows what is running, as tmux's
	// current window does.
	m := resolve(n.command, "")
	return []SessionInfo{{Name: n.Session(), Windows: 1, CurrentWindow: n.command, Icon: m.Icon, Command: m.Command}}
}
func (n *None) HasSession(context.Context, string) bool                   { return true }
func (n *None) CreateSession(context.Context, string) error               { return nil }
func (n *None) KillSession(context.Context, string) error                 { return nil }
func (n *None) AttachArgs(string, string) []string                        { return []string{} }
func (n *None) ListWindows(context.Context, string, string) []WindowInfo  { return []WindowInfo{} }
func (n *None) Run(context.Context, string, Action, string, string) error { return nil }

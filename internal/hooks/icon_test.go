package hooks

import (
	"testing"

	"github.com/sophonz-labs/stui/internal/cmdicon"
)

// A hooked agent's pane runs under whatever its process is called (Pi and
// Kimi Code show as node), so its icon comes from the kind its hooks name.
// Without an entry under that name the window showed Node.js.
func TestEveryHookedAgentHasAnIcon(t *testing.T) {
	for _, a := range Agents() {
		m := cmdicon.Resolve(a.ID, "node", nil)
		if m.Command != a.ID || m.Icon == "nodedotjs" {
			t.Errorf("%s: an agent pane stui has down as %s draws %q as %q; add it to internal/cmdicon/registry.json", a.Name, a.ID, m.Icon, m.Command)
		}
	}
}

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/sophonz-labs/stui/internal/orch"
)

// workerRename is stui worker rename <id> <name>: a worker's label, which
// adopt took from the tmux window title. The window keeps its title.
func workerRename(rest []string, asJSON bool) {
	var words []string
	for _, w := range rest {
		if !strings.HasPrefix(w, "--") {
			words = append(words, w)
		}
	}
	if len(words) != 2 {
		die(asJSON, map[string]any{}, "stui worker rename <id> <name>   (%s)", idForms)
	}
	_, s := here(asJSON)
	id := resolveWorker(words[0], asJSON)
	name, err := orch.CheckName(words[1])
	if err != nil {
		die(asJSON, map[string]any{"id": id, "reason": "bad-name"}, "%v", err)
	}
	var res map[string]any
	code, err := apiDo(s, "POST", "/api/orch/name", map[string]any{"pane": id, "name": name, "by": os.Getenv("TMUX_PANE")}, &res)
	if err != nil || code != 200 {
		why := fmt.Sprint(res["error"])
		if code == 404 && res["error"] == nil {
			why = "this stui does not rename workers yet: a stui server older than this binary is running; stui stop, then try again"
		}
		receipt := map[string]any{"id": id, "result": res}
		if r, ok := res["reason"]; ok {
			receipt["reason"] = r
		}
		die(asJSON, receipt, "not renamed (%d %v): %s", code, err, why)
	}
	if asJSON {
		printJSON(res)
		return
	}
	if was := fmt.Sprint(res["was"]); was != "" && was != "<nil>" && was != name {
		fmt.Printf("  %s is %s now (was %s)\n", id, name, was)
	} else {
		fmt.Printf("  %s is %s\n", id, name)
	}
}

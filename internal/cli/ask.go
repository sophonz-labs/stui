package cli

// stui worker question and stui worker reply (orch/ask.go): a question to
// another pane that waits for its answer, and the answer. Not stui worker ask,
// which asks a reading agent about a worktree (worktree.go).

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
)

// words is a verb's words that are not flags or their values.
func words(rest []string) []string {
	var out []string
	for i := 0; i < len(rest); i++ {
		if strings.HasPrefix(rest[i], "-") {
			if valued[rest[i]] {
				i++
			}
			continue
		}
		out = append(out, rest[i])
	}
	return out
}

func waitFor(a args, asJSON bool) time.Duration {
	t, ok := a.flag("--timeout")
	if !ok {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(t)
	if err != nil || d <= 0 || d > 30*time.Minute {
		die(asJSON, map[string]any{}, "--timeout takes a duration up to 30m, like 10m")
	}
	return d
}

func workerQuestion(a args, rest []string, asJSON bool) {
	me, _ := here(asJSON)
	var ask orch.Event
	if id, ok := a.flag("--resume"); ok {
		ev, found := orch.FindEvent(id)
		if !found || ev.Kind != orch.EvAsk || ev.From != me {
			die(asJSON, map[string]any{}, "no question %s asked from %s", id, me)
		}
		ask = ev
	} else {
		w := words(rest)
		var to, q string
		switch len(w) {
		case 1:
			to, q = paneOpt(me, "@stui-parent"), w[0]
			if to == "" {
				die(asJSON, map[string]any{}, "%s has no coordinator to ask: stui worker question <id> \"<question>\"", me)
			}
		case 2:
			to, q = resolveWorker(w[0], asJSON), w[1]
		default:
			die(asJSON, map[string]any{}, `stui worker question [<id>] "<question>" [--timeout 10m]; stui worker question --resume <question id>`)
		}
		if strings.Contains(to, "/") {
			die(asJSON, map[string]any{}, "%s is on another machine: stui worker question reaches a pane on this one only, for now", to)
		}
		if strings.TrimSpace(q) == "" {
			die(asJSON, map[string]any{}, "the question is empty")
		}
		run := firstRun(to, me)
		if run == "" {
			die(asJSON, map[string]any{}, "%s and %s are in no run: stui worker question is between a coordinator and its workers", me, to)
		}
		var err error
		if ask, err = orch.Ask(run, me, paneName(me), to, paneName(to), q); err != nil {
			die(asJSON, map[string]any{}, "%v", err)
		}
		if !asJSON {
			fmt.Printf("  asked %s (question %s); waiting for the answer\n", named(to), ask.ID)
		}
	}
	// The carrier does not type at a pane that waits (orch/inbox.go): the
	// answer wakes this.
	defer orch.Waiting(me)()
	reply, ok := orch.WaitReply(ask, time.Now().Add(waitFor(a, asJSON)), time.Second)
	if !ok {
		if asJSON {
			printJSON(map[string]any{"id": ask.ID, "answered": false, "timeout": true})
		} else {
			fmt.Printf("  no answer yet: a checkpoint, not a failure. stui worker question --resume %s waits again\n", ask.ID)
		}
		os.Exit(2)
	}
	// Read here: not typed in as well.
	orch.Ack(me, []orch.Mail{{Event: reply}})
	answer := orch.FullText(reply)
	if asJSON {
		printJSON(map[string]any{"id": ask.ID, "answered": true, "reply": reply.ID, "from": reply.From, "by": reply.By, "answer": answer})
		return
	}
	who := firstOf(reply.FromName, firstOf(reply.From, "the app"))
	fmt.Printf("  %s answered:\n%s\n", who, answer)
}

func workerReply(rest []string, asJSON bool) {
	me, _ := here(asJSON)
	w := words(rest)
	if len(w) != 2 {
		die(asJSON, map[string]any{}, `stui worker reply <question id> "<answer>"`)
	}
	ask, found := orch.FindEvent(w[0])
	if !found || ask.Kind != orch.EvAsk {
		die(asJSON, map[string]any{}, "no question %s (stui worker inbox lists the ones for you)", w[0])
	}
	if r, done := orch.ReplyTo(ask); done {
		die(asJSON, map[string]any{}, "%s was answered at %s", ask.ID, time.UnixMilli(r.TS).Local().Format("15:04"))
	}
	if strings.TrimSpace(w[1]) == "" {
		die(asJSON, map[string]any{}, "the answer is empty")
	}
	reply, err := orch.Reply(ask, me, paneName(me), "", w[1])
	if err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	waits := orch.IsWaiting(ask.From)
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": reply.ID, "to": ask.From, "waiting": waits})
		return
	}
	if waits {
		fmt.Printf("  answered %s; it was waiting and has it now\n", named(ask.From))
	} else {
		fmt.Printf("  answered %s; it is told between its turns\n", named(ask.From))
	}
}

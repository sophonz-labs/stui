package cli

// stui worker inbox: the mail owed to this pane (orch/inbox.go) -- work that
// stui worker send could not type, and workers' news whose line was not seen
// to go in. Showing it takes it; --peek only shows.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/state"
)

// mailOwner is who mail is for, as the carrier reckons it.
func mailOwner(s *state.Agent) orch.MailOwner {
	out, _ := tx("list-panes", "-a", "-F", orch.MailFormat)
	states := agentPanes(s)
	return orch.OwnerFrom(out, func(id string) (string, int64) {
		p := states[id]
		return p.State, p.Since
	})
}

func workerInbox(a args, asJSON bool) {
	pane, s := here(asJSON)
	owner := mailOwner(s)
	mail := orch.Inbox(pane, owner)
	if a.has("--wait") && len(mail) == 0 {
		timeout := 10 * time.Minute
		if t, ok := a.flag("--timeout"); ok {
			d, err := time.ParseDuration(t)
			if err != nil || d <= 0 {
				die(asJSON, map[string]any{}, "--timeout takes a duration, like 10m")
			}
			timeout = d
		}
		// The carrier does not type at a pane that waits here (waiter
		// first): the mail wakes it.
		done := orch.Waiting(pane)
		mail = orch.WaitMail(pane, func(ev orch.Event) string { return mailOwner(s)(ev) }, time.Now().Add(timeout), time.Second)
		done()
		if len(mail) == 0 {
			if asJSON {
				printJSON(map[string]any{"mail": []any{}, "timeout": true})
			} else {
				fmt.Println("  nothing came: a checkpoint, not a failure. stui worker inbox --wait again")
			}
			os.Exit(2)
		}
	}
	var acked []string
	switch {
	case a.has("--peek"):
	case a.has("--ack-all"):
		acked = orch.Ack(pane, mail)
	default:
		if id, ok := a.flag("--ack"); ok {
			var one []orch.Mail
			for _, m := range mail {
				if m.ID == id {
					one = append(one, m)
				}
			}
			if len(one) == 0 {
				die(asJSON, map[string]any{}, "no mail %s for %s (stui worker inbox --peek lists it)", id, pane)
			}
			acked = orch.Ack(pane, one)
			mail = one
		} else {
			acked = orch.Ack(pane, mail)
		}
	}
	if asJSON {
		rows := []map[string]any{}
		for _, m := range mail {
			rows = append(rows, mailRow(m))
		}
		printJSON(map[string]any{"mail": rows, "acked": acked})
		return
	}
	if len(mail) == 0 {
		fmt.Println("  nothing for you")
		return
	}
	for _, m := range mail {
		fmt.Println("  " + mailText(m))
	}
	if a.has("--peek") {
		fmt.Println("  (not taken: stui worker inbox takes them)")
	}
}

func mailRow(m orch.Mail) map[string]any {
	r := map[string]any{"id": m.ID, "run": m.Run, "kind": m.Kind, "ts": m.TS, "from": m.From, "fromName": m.FromName, "title": m.Title, "pointed": m.Pointed}
	if m.File != "" {
		r["file"] = m.File
	}
	if m.Doc != "" {
		r["doc"] = m.Doc
	}
	if m.Kind == orch.EvIdle {
		r["stopReason"], r["why"] = m.StopReason, m.Why
	}
	return r
}

// mailText is one piece of mail as a line: when, who, and how to read it.
func mailText(m orch.Mail) string {
	at := time.UnixMilli(m.TS).Local().Format("15:04")
	who := m.FromName
	if who == "" {
		who = m.From
	}
	switch m.Kind {
	case orch.EvSend:
		what := m.Title
		if m.File != "" {
			what = fmt.Sprintf("%q is in %s . Read it and do it.", m.Title, m.File)
		} else if m.Text != "" {
			what = m.Text
		}
		return fmt.Sprintf("%s  from %s (%s): %s", at, firstOf(who, "?"), m.From, what)
	}
	if m.Kind == orch.EvAsk {
		return fmt.Sprintf("%s  %s (%s) asks (%s):\n%s\n  answer: stui worker reply %s \"<answer>\"", at, firstOf(who, "?"), m.From, m.ID, orch.FullText(m.Event), m.ID)
	}
	if m.Kind == orch.EvReply {
		return fmt.Sprintf("%s  %s answered your question %s:\n%s", at, firstOf(who, firstOf(m.By, "?")), m.Ref, orch.FullText(m.Event))
	}
	line := orch.MailLine(m.Event, "", "")
	return at + "  " + strings.TrimPrefix(line, "[stui] ")
}

// owedLater is whether a send turned down for reason may go in later: the
// box is busy for now, not gone or not an agent.
func owedLater(reason string) bool {
	switch reason {
	case chat.BusyInput, chat.PromptPending, chat.Sending, chat.NoInput:
		return true
	}
	return false
}

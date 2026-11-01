package orch

// Asking and answering: stui worker question puts
// a question to another pane -- a worker to its coordinator, usually -- and
// waits for the answer; stui worker reply answers it. Both are mail (inbox.go):
// the question is typed into the pane it is for between its turns, or
// waits in its inbox; the answer wakes the one waiting, or, if that one has
// stopped waiting, is mail for it in turn.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// EvAsk is a question: From asks To. Text is its start, File the whole
	// of it when it is longer.
	EvAsk = "ask"
	// EvReply answers the ask in Ref: From (or By, "app") to To, the asker.
	EvReply = "reply"
)

// A question or answer longer than this is kept whole in the run's sent/,
// as a brief is: an event's text is cut at 400.
const askInline = 300

// keepText is the event's text, and where the whole of it is kept when it
// is longer than an event holds.
func keepText(run, what, text string) (string, string) {
	if len([]rune(text)) <= askInline {
		return text, ""
	}
	dir := filepath.Join(RunDir(run), "sent")
	if os.MkdirAll(dir, 0o700) != nil {
		return text, ""
	}
	f := filepath.Join(dir, time.Now().Format("20060102-150405.000")+"-"+what+".md")
	if os.WriteFile(f, []byte(text), 0o600) != nil {
		return text, ""
	}
	return text, f
}

// FullText is what an ask or reply said, whole.
func FullText(ev Event) string {
	if ev.File != "" {
		if b, err := os.ReadFile(ev.File); err == nil {
			return string(b)
		}
	}
	return ev.Text
}

// Ask writes from's question to to.
func Ask(run, from, fromName, to, toName, question string) (Event, error) {
	text, file := keepText(run, "ask", question)
	return AppendEvent(Event{Run: run, Kind: EvAsk, From: from, FromName: fromName, To: to, ToName: toName, Up: []string{to}, Title: FirstLine(question), Text: text, File: file})
}

// FindEvent is the event with id in a recent run.
func FindEvent(id string) (Event, bool) {
	for _, run := range RecentRuns(mailRuns) {
		for _, ev := range ReadEvents(run) {
			if ev.ID == id {
				return ev, true
			}
		}
	}
	return Event{}, false
}

// ReplyTo is the answer to the ask with id, if there is one.
func ReplyTo(ask Event) (Event, bool) {
	for _, ev := range ReadEvents(ask.Run) {
		if ev.Kind == EvReply && ev.Ref == ask.ID {
			return ev, true
		}
	}
	return Event{}, false
}

// Reply answers ask: from a pane, or with by ("app") from an app. The ask
// is taken with it.
func Reply(ask Event, from, fromName, by, answer string) (Event, error) {
	text, file := keepText(ask.Run, "reply", answer)
	ev, err := AppendEvent(Event{Run: ask.Run, Kind: EvReply, Ref: ask.ID, From: from, FromName: fromName, By: by, To: ask.From, ToName: ask.FromName, Up: []string{ask.From}, Title: FirstLine(answer), Text: text, File: file})
	if err != nil {
		return ev, err
	}
	taker := from
	if taker == "" {
		taker = by
	}
	AppendEvent(Event{Run: ask.Run, Kind: EvAcked, Ref: ask.ID, From: taker})
	return ev, nil
}

// WaitReply blocks until ask is answered or until is past, polling.
func WaitReply(ask Event, until time.Time, every time.Duration) (Event, bool) {
	for {
		if r, ok := ReplyTo(ask); ok {
			return r, true
		}
		if !time.Now().Before(until) {
			return Event{}, false
		}
		time.Sleep(every)
	}
}

// askLine and replyLine are the lines typed for them (mail.go).
func askLine(ev Event) string {
	who := firstNonEmpty(ev.FromName, ev.From)
	if ev.FromName != "" {
		who += " (" + ev.From + ")"
	}
	more := ""
	if ev.File != "" || strings.Contains(strings.TrimSpace(ev.Text), "\n") || len([]rune(ev.Title)) > 120 {
		more = " (stui worker inbox shows all of it)"
	}
	return fmt.Sprintf("[stui] %s asks: %s%s -- answer: stui worker reply %s \"<answer>\"", who, clip(ev.Title, 120), more, ev.ID)
}

func replyLine(ev Event) string {
	who := firstNonEmpty(ev.FromName, firstNonEmpty(ev.From, "the app"))
	if ev.FromName != "" && ev.From != "" {
		who += " (" + ev.From + ")"
	}
	return fmt.Sprintf("[stui] %s answered your question: %s -- stui worker question --resume %s shows all of it", who, clip(ev.Title, 120), ev.Ref)
}

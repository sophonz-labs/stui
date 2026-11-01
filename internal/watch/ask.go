package watch

// Answering Claude Code's AskUserQuestion whole: every question of a
// multi-question prompt, and several choices of a multi-select one, in one
// answer.
//
// The screen shows one question at a time, so what a client needs -- all of
// them, which take several choices -- comes from the tool call itself, which
// stui's PreToolUse hook carries. The keys and their pacing are Orca's
// (src/shared/native-chat-ask.ts buildAskAnswerKeys and
// src/shared/native-chat-answer-stepping.ts, MIT, Copyright (c) 2026
// Lovecast Inc.; see NOTICE): an option's number picks it (and
// moves a single-select on to the next question), Right moves past a
// multi-select or a skipped question, and the last step is Enter on the
// Submit tab. Each group goes a step after the one before, or the selector
// commits before it has applied the key.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// AskQuestion is one question of an AskUserQuestion call.
type AskQuestion struct {
	Header      string      `json:"header,omitempty"`
	Question    string      `json:"question"`
	Options     []AskOption `json:"options"`
	MultiSelect bool        `json:"multiSelect"`
}

type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskAnswer answers one question: the options picked (indexes into its
// options; one for a single-select), and text typed into its "Type
// something" row.
type AskAnswer struct {
	Options []int  `json:"options"`
	Text    string `json:"text,omitempty"`
}

// askCall is the AskUserQuestion the main agent is in, from its PreToolUse.
type askCall struct {
	questions []AskQuestion
	at        time.Time
}

// askFresh is how long a call's questions stand without its PostToolUse: a
// question can sit unanswered for a long time, not for ever.
const askFresh = 24 * time.Hour

const (
	// Orca's NATIVE_CHAT_SUBMIT_DELAY_MS + NATIVE_CHAT_ADVANCE_BUFFER_MS: one
	// group, then the selector's time to apply it and draw the next step.
	askSubmitDelay = 500 * time.Millisecond
	askAdvance     = 500 * time.Millisecond
)

var askStep = askSubmitDelay + askAdvance

// The most an answer may say: AskUserQuestion asks at most four questions
// with at most four options each.
const (
	maxAskQuestions = 8
	maxAskOptions   = 9
)

// parseAsk reads the questions out of the hook's tool_input, which is JSON
// text (hookclient). nil when it is not an AskUserQuestion input, or was cut
// short to fit.
func parseAsk(raw any) []AskQuestion {
	s, ok := raw.(string)
	if !ok || s == "" {
		return nil
	}
	var in struct {
		Questions []AskQuestion `json:"questions"`
	}
	if json.Unmarshal([]byte(s), &in) != nil || len(in.Questions) == 0 || len(in.Questions) > maxAskQuestions {
		return nil
	}
	for i := range in.Questions {
		q := &in.Questions[i]
		if strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 || len(q.Options) > maxAskOptions {
			return nil
		}
		q.Header, q.Question = clipText(q.Header, 120), clipText(q.Question, 500)
		for j := range q.Options {
			q.Options[j].Label = clipText(q.Options[j].Label, 200)
			q.Options[j].Description = clipText(q.Options[j].Description, 500)
		}
	}
	return in.Questions
}

// noteAskLocked keeps the main agent's AskUserQuestion between its PreToolUse
// and its PostToolUse.
func (p *paneState) noteAskLocked(event string, data map[string]any, now time.Time) {
	switch event {
	case "PreToolUse":
		if qs := parseAsk(data["tool_input"]); qs != nil {
			p.ask = &askCall{questions: qs, at: now}
		} else {
			p.ask = nil
		}
	case "PostToolUse", "PostToolUseFailure":
		p.ask = nil
	}
}

// askFor is the questions still to answer when the screen shows parsed: from
// the one on screen to the last. nil when the screen is not one of them (the
// review step at the end, or another prompt altogether).
func askFor(a *askCall, parsed *Parsed, now time.Time) []AskQuestion {
	if a == nil || parsed == nil || now.Sub(a.at) > askFresh || len(parsed.Options) == 0 {
		return nil
	}
	body := squeeze(parsed.Body, parsed.Question)
	first := squeeze(parsed.Options[0].Label)
	for i, q := range a.questions {
		if !strings.Contains(body, squeeze(q.Question)) {
			continue
		}
		if !strings.Contains(first, squeeze(q.Options[0].Label)) {
			continue
		}
		return a.questions[i:]
	}
	return nil
}

// askMulti is whether any question takes several choices.
func askMulti(qs []AskQuestion) bool {
	for _, q := range qs {
		if q.MultiSelect {
			return true
		}
	}
	return false
}

// askKeys is the send-keys groups for answers to qs, in order; false when the
// answers do not fit the questions or answer none of them.
func askKeys(qs []AskQuestion, answers []AskAnswer) ([][]string, bool) {
	if len(qs) == 0 || len(answers) != len(qs) {
		return nil, false
	}
	answered := false
	for i, a := range answers {
		seen := map[int]bool{}
		for _, n := range a.Options {
			if n < 0 || n >= len(qs[i].Options) || seen[n] {
				return nil, false
			}
			seen[n] = true
		}
		if !qs[i].MultiSelect && len(a.Options) > 1 {
			return nil, false
		}
		t := askText(a.Text)
		if len([]rune(t)) > 4000 {
			return nil, false
		}
		if len(a.Options) > 0 || t != "" {
			answered = true
		}
	}
	if !answered {
		return nil, false
	}

	multiQuestion := len(qs) > 1
	var groups [][]string
	for i, q := range qs {
		a := answers[i]
		text := askText(a.Text)
		typeSomething := strconv.Itoa(len(q.Options) + 1)
		switch {
		case q.MultiSelect:
			// Each number toggles its box; a multi-select never moves on by
			// itself.
			for _, n := range a.Options {
				groups = append(groups, []string{strconv.Itoa(n + 1)})
			}
			if text != "" {
				groups = append(groups, []string{typeSomething}, []string{"-l", text}, []string{"Enter"})
			}
			groups = append(groups, []string{"Right"})
		case text != "":
			// A single-select carries one value: the picked label and the
			// text, through "Type something".
			var parts []string
			for _, n := range a.Options {
				parts = append(parts, q.Options[n].Label)
			}
			parts = append(parts, text)
			groups = append(groups, []string{typeSomething}, []string{"-l", strings.Join(parts, ", ")}, []string{"Enter"})
		case len(a.Options) > 0:
			groups = append(groups, []string{strconv.Itoa(a.Options[0] + 1)})
		case multiQuestion:
			// Not answered: step past it.
			groups = append(groups, []string{"Right"})
		}
	}
	// A multi-question prompt, and a lone multi-select, end on the Submit tab.
	if (multiQuestion || qs[0].MultiSelect) && len(groups) > 0 {
		groups = append(groups, []string{"Enter"})
	}
	return groups, true
}

// askText is free text for the "Type something" row: typed as keys, where a
// newline would submit it early, so lines become spaces.
func askText(s string) string {
	s = cleanText(s)
	return strings.TrimSpace(strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " "))
}

// askAnswerKey is what makes two step answers the same, for a retry.
func askAnswerKey(answers []AskAnswer) string {
	b, _ := json.Marshal(answers)
	return "ask\x00" + string(b)
}

// AnswerSteps answers every question of an AskUserQuestion prompt at once
// (POST /api/prompts/<id>/answer with "answers").
func (w *Watcher) AnswerSteps(id string, answers []AskAnswer) (int, map[string]any) {
	return w.carryOut(id, askAnswerKey(answers), func(pr Prompt) ([][]string, bool) {
		if len(pr.Questions) == 0 {
			return nil, false
		}
		return askKeys(pr.Questions, answers)
	}, askStep)
}

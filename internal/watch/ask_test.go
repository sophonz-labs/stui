package watch

import (
	"strings"
	"testing"
	"time"
)

const askInput = `{"questions":[` +
	`{"header":"Color","question":"Which color do you prefer for the logo?","multiSelect":false,"options":[` +
	`{"label":"Red","description":"Bold and energetic"},{"label":"Green","description":"Fresh and natural"},{"label":"Blue","description":"Calm and professional"}]},` +
	`{"header":"Extras","question":"Which extras should it have?","multiSelect":true,"options":[` +
	`{"label":"Shadow"},{"label":"Outline"},{"label":"Gradient"}]}]}`

func keysOf(groups [][]string) string {
	var out []string
	for _, g := range groups {
		out = append(out, strings.Join(g, " "))
	}
	return strings.Join(out, "|")
}

// Orca's buildAskAnswerKeys cases, in stui's send-keys words.
func TestAskKeys(t *testing.T) {
	single := AskQuestion{Question: "Pick", Options: []AskOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}}
	multi := AskQuestion{Question: "Many", MultiSelect: true, Options: []AskOption{{Label: "X"}, {Label: "Y"}, {Label: "Z"}}}
	for _, c := range []struct {
		name string
		qs   []AskQuestion
		as   []AskAnswer
		want string
	}{
		{"one pick commits", []AskQuestion{single}, []AskAnswer{{Options: []int{1}}}, "2"},
		{"text goes through Type something", []AskQuestion{single}, []AskAnswer{{Text: "  pink\nish "}}, "4|-l pink ish|Enter"},
		{"pick and text are one value", []AskQuestion{single}, []AskAnswer{{Options: []int{0}, Text: "but darker"}}, "4|-l A, but darker|Enter"},
		{"lone multi-select ends on Submit", []AskQuestion{multi}, []AskAnswer{{Options: []int{0, 2}}}, "1|3|Right|Enter"},
		{"multi-select with text", []AskQuestion{multi}, []AskAnswer{{Options: []int{1}, Text: "glow"}}, "2|4|-l glow|Enter|Right|Enter"},
		{"two questions", []AskQuestion{single, multi}, []AskAnswer{{Options: []int{2}}, {Options: []int{1}}}, "3|2|Right|Enter"},
		{"a skipped question is stepped past", []AskQuestion{single, single}, []AskAnswer{{}, {Options: []int{0}}}, "Right|1|Enter"},
	} {
		got, ok := askKeys(c.qs, c.as)
		if !ok || keysOf(got) != c.want {
			t.Errorf("%s: %v %q, want %q", c.name, ok, keysOf(got), c.want)
		}
	}
	for name, as := range map[string][]AskAnswer{
		"nothing answered":     {{}},
		"two for a single":     {{Options: []int{0, 1}}},
		"out of range":         {{Options: []int{3}}},
		"negative":             {{Options: []int{-1}}},
		"answers for too many": {{Options: []int{0}}, {Options: []int{0}}},
		"text far too long":    {{Text: strings.Repeat("a", 4001)}},
	} {
		if _, ok := askKeys([]AskQuestion{single}, as); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, ok := askKeys([]AskQuestion{multi}, []AskAnswer{{Options: []int{1, 1}}}); ok {
		t.Error("the same box twice toggles it off: refused")
	}
}

func TestParseAsk(t *testing.T) {
	qs := parseAsk(askInput)
	if len(qs) != 2 || qs[0].Header != "Color" || len(qs[0].Options) != 3 || qs[0].Options[1].Description != "Fresh and natural" || !qs[1].MultiSelect {
		t.Fatalf("%+v", qs)
	}
	// Cut short by the hook to fit: not a question to answer whole.
	if parseAsk(askInput[:len(askInput)-20]+"…") != nil {
		t.Fatal("clipped input parsed")
	}
	if parseAsk(`{"command":"ls"}`) != nil || parseAsk(nil) != nil {
		t.Fatal("not an AskUserQuestion")
	}
}

func TestAskForMatchesTheQuestionOnScreen(t *testing.T) {
	now := time.Now()
	a := &askCall{questions: parseAsk(askInput), at: now}
	parsed := ParseScreen(screen(t, "claude-question-multi.txt"), "claude")
	if parsed == nil {
		t.Fatal("no prompt read")
	}
	if qs := askFor(a, parsed, now); len(qs) != 2 {
		t.Fatalf("%+v", qs)
	}
	// The second question on screen: only it is left.
	second := strings.NewReplacer("Which color do you prefer for the logo?", "Which extras should it have?",
		"1. Red", "1. [ ] Shadow", "2. Green", "2. [ ] Outline", "3. Blue", "3. [ ] Gradient").Replace(screen(t, "claude-question-multi.txt"))
	if qs := askFor(a, ParseScreen(second, "claude"), now); len(qs) != 1 || qs[0].Header != "Extras" {
		t.Fatalf("%+v", qs)
	}
	// Another question altogether, and a call long gone.
	if askFor(a, ParseScreen(screen(t, "claude-permission-bash.txt"), "claude"), now) != nil {
		t.Fatal("matched another question")
	}
	if askFor(a, parsed, now.Add(askFresh+time.Minute)) != nil {
		t.Fatal("stale call matched")
	}
}

// A prompt stui did not hear the call for answers as before, one option.
func TestAnswerStepsNeedQuestions(t *testing.T) {
	w, f, _ := setup(t, "2.1.283")
	f.setScreen("%1", screen(t, "claude-question.txt"))
	hook(w, "Notification", map[string]any{"notification_type": "elicitation_dialog"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	pr := pane(t, w, "%1").Prompt
	if len(pr.Questions) != 0 {
		t.Fatalf("%+v", pr.Questions)
	}
	if status, _ := w.AnswerSteps(pr.ID, []AskAnswer{{Options: []int{0}}}); status != 400 {
		t.Fatal(status)
	}
}

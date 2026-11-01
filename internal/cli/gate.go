package cli

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// stui gate: a decision only a person can make. The agent asks and stops
// its turn; the person answers with stui gate answer; stui types the answer
// into the agent as its next message.

const gateHelp = `stui gate -- ask a person to decide

  stui gate ask "<question>" --option "<A>" --option "<B>" [--title "<few words>"] [--text]
                                   ask from this pane; then stop your turn: the answer comes
                                   as your next message ("Decision gate <id> answered: …")
  stui gate list [--all]             open gates (with --all, answered and cancelled ones too)
  stui gate show <id>                one gate and its answer
  stui gate answer <id> <n|"text">   answer it here (n counts from 1)
  stui gate cancel <id>              take it back

--text lets the answer be words instead of (or besides) an option.
Every command takes --json.
`

func gateCommand(a args) {
	sub := a.after("gate")
	asJSON := a.has("--json")
	if len(sub) == 0 {
		fmt.Print(gateHelp)
		return
	}
	switch sub[0] {
	case "ask":
		gateAsk(a, sub[1:], asJSON)
	case "list":
		gateList(a, asJSON)
	case "show":
		gateShow(sub[1:], asJSON)
	case "answer":
		gateAnswer(sub[1:], asJSON)
	case "cancel":
		gateCancel(sub[1:], asJSON)
	default:
		fmt.Print(gateHelp)
		os.Exit(1)
	}
}

// repeated is every value of a flag given more than once.
func repeated(a args, name string) []string {
	var out []string
	for i := 0; i+1 < len(a); i++ {
		if a[i] == name {
			out = append(out, a[i+1])
		}
	}
	return out
}

func gateAsk(a args, rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
		die(asJSON, map[string]any{}, `stui gate ask "<question>" --option "<A>" --option "<B>"`)
	}
	pane, s := here(asJSON)
	title, _ := a.flag("--title")
	body := map[string]any{"pane": pane, "title": title, "question": rest[0], "options": repeated(a, "--option"), "allowText": a.has("--text")}
	var res map[string]any
	code, err := apiDo(s, "POST", "/api/gates", body, &res)
	if err != nil || code != 200 {
		why := fmt.Sprint(res["error"])
		if code == 404 {
			why = "this stui has no decision gates yet: a stui server older than this binary is running; stui stop, then try again"
		}
		die(asJSON, map[string]any{"result": res}, "not asked (%d %v): %s", code, err, why)
	}
	if asJSON {
		printJSON(res)
		return
	}
	fmt.Printf("  gate %v is open: a person answers it with stui gate answer.\n", res["id"])
	fmt.Println("  Stop your turn now. The answer comes as your next message.")
}

type gateRow struct {
	ID         string   `json:"id"`
	Pane       string   `json:"pane"`
	Title      string   `json:"title"`
	Question   string   `json:"question"`
	Options    []string `json:"options"`
	Status     string   `json:"status"`
	Answer     string   `json:"answer"`
	Told       string   `json:"told"`
	CreatedAt  int64    `json:"createdAt"`
	AnsweredAt int64    `json:"answeredAt"`
}

func gateList(a args, asJSON bool) {
	_, s := here(asJSON)
	var res struct {
		Gates []gateRow `json:"gates"`
	}
	if code, err := apiDo(s, "GET", "/api/gates", nil, &res); err != nil || code != 200 {
		die(asJSON, map[string]any{}, "could not read the gates (%d %v)", code, err)
	}
	var rows []gateRow
	for _, g := range res.Gates {
		if a.has("--all") || g.Status == "open" {
			rows = append(rows, g)
		}
	}
	if asJSON {
		printJSON(map[string]any{"gates": rows})
		return
	}
	if len(rows) == 0 {
		fmt.Println("  no open gates")
		return
	}
	for _, g := range rows {
		line := fmt.Sprintf("  %s %-5s %-9s %s", g.ID, g.Pane, g.Status, g.Question)
		if g.Answer != "" {
			line += " → " + g.Answer
		}
		fmt.Println(line)
	}
}

func gateShow(rest []string, asJSON bool) {
	if len(rest) == 0 {
		die(asJSON, map[string]any{}, "stui gate show <id>")
	}
	_, s := here(asJSON)
	var g gateRow
	if code, err := apiDo(s, "GET", "/api/gates/"+url.PathEscape(rest[0]), nil, &g); err != nil || code != 200 {
		die(asJSON, map[string]any{}, "no gate %s (%d %v)", rest[0], code, err)
	}
	if asJSON {
		printJSON(g)
		return
	}
	fmt.Printf("  %s in %s: %s\n  %s\n", g.ID, g.Pane, g.Status, g.Question)
	for i, o := range g.Options {
		fmt.Printf("    %d. %s\n", i+1, o)
	}
	if g.Answer != "" {
		fmt.Printf("  answer: %s (told: %s)\n", g.Answer, g.Told)
	}
}

func gateAnswer(rest []string, asJSON bool) {
	if len(rest) < 2 {
		die(asJSON, map[string]any{}, `stui gate answer <id> <n|"text">`)
	}
	_, s := here(asJSON)
	body := map[string]any{}
	if n, err := strconv.Atoi(rest[1]); err == nil {
		body["option"] = n - 1
		if len(rest) > 2 {
			body["text"] = strings.Join(rest[2:], " ")
		}
	} else {
		body["text"] = strings.Join(rest[1:], " ")
	}
	var res map[string]any
	code, err := apiDo(s, "POST", "/api/prompts/"+url.PathEscape(rest[0])+"/answer", body, &res)
	if err != nil || code != 200 {
		die(asJSON, map[string]any{"result": res}, "not answered (%d %v): %v", code, err, res["error"])
	}
	if asJSON {
		printJSON(res)
		return
	}
	fmt.Printf("  answered: %v (told: %v)\n", res["answer"], res["told"])
}

func gateCancel(rest []string, asJSON bool) {
	if len(rest) == 0 {
		die(asJSON, map[string]any{}, "stui gate cancel <id>")
	}
	_, s := here(asJSON)
	var res map[string]any
	code, err := apiDo(s, "POST", "/api/gates/"+url.PathEscape(rest[0])+"/cancel", map[string]any{}, &res)
	if err != nil || code != 200 {
		die(asJSON, map[string]any{"result": res}, "not cancelled (%d %v): %v", code, err, res["error"])
	}
	if asJSON {
		printJSON(res)
		return
	}
	fmt.Printf("  %s %v\n", rest[0], res["status"])
}

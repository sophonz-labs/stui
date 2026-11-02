package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// A placeholder is {name}: a letter or _, then letters, digits, _ . -.
// Any other { is itself, so jq's {a: .b} needs no escape; {{ is a
// literal { for the rare text that looks like a placeholder.
var phRe = regexp.MustCompile(`\{\{|\{[A-Za-z_][A-Za-z0-9_.-]*\}`)

// Placeholders is the names in s, in order.
func Placeholders(s string) []string {
	var out []string
	for _, m := range phRe.FindAllString(s, -1) {
		if m != "{{" {
			out = append(out, m[1:len(m)-1])
		}
	}
	return out
}

// Expand fills an argv's placeholders. Every value goes inside the one
// element it is in and is never split, so a row field holding
// "x; rm -rf ~" is one argument and runs nothing. The one exception is
// an element that is exactly {args}: it becomes the command's words, one
// element each (none when there are none). An unknown name is an error
// and nothing runs.
func Expand(argv []string, vars map[string]string, args []string) ([]string, error) {
	out := make([]string, 0, len(argv))
	for _, s := range argv {
		if s == "{args}" {
			out = append(out, args...)
			continue
		}
		var err error
		r := phRe.ReplaceAllStringFunc(s, func(m string) string {
			if m == "{{" {
				return "{"
			}
			n := m[1 : len(m)-1]
			if n == "args" {
				return strings.Join(args, " ")
			}
			v, ok := vars[n]
			if !ok && err == nil {
				err = fmt.Errorf("no {%s} here", n)
			}
			return v
		})
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

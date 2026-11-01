package mux

import (
	"strings"
	"testing"
)

// Without a UTF-8 locale tmux prints a format's tabs as "_".
func TestUTF8Env(t *testing.T) {
	cases := []struct {
		env  []string
		adds bool
	}{
		{[]string{"PATH=/bin"}, true},
		{[]string{"LANG=C"}, true},
		{[]string{"LANG=ko_KR.UTF-8"}, false},
		{[]string{"LC_CTYPE=UTF-8", "LANG=C"}, false},
		{[]string{"LC_ALL=en_US.utf8"}, false},
	}
	for _, c := range cases {
		got := UTF8Env(c.env)
		added := len(got) > len(c.env) && strings.HasPrefix(got[len(got)-1], "LC_CTYPE=")
		if added != c.adds {
			t.Errorf("%v -> %v", c.env, got)
		}
	}
}

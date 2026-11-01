package mux

import (
	"os"
	"runtime"
	"strings"
)

// EnsureUTF8 gives this process, and what it starts, a UTF-8 character
// type when its environment names none.
//
// A tmux client decides whether its terminal is UTF-8 from LC_ALL,
// LC_CTYPE and LANG, and without one that says so it prints every tab of a
// -F format as "_" (tmux 3.7c and 3.8 alike). stui reads tab-separated
// formats in many places -- run records, worker mail, adoption -- and
// launchd and systemd start the agent with no LANG at all, so after a
// restart under the service those all read nothing. A tmux
// server stui starts hands the variable on to its shells, which then show
// non-ASCII names instead of escapes as well.
func EnsureUTF8() {
	if !utf8Locale(os.Getenv) {
		os.Setenv("LC_CTYPE", utf8CType())
	}
}

// UTF8Env is environ with the same fix: for a tmux run with an environment
// of its own.
func UTF8Env(environ []string) []string {
	get := func(k string) string {
		for i := len(environ) - 1; i >= 0; i-- {
			if n, v, ok := strings.Cut(environ[i], "="); ok && n == k {
				return v
			}
		}
		return ""
	}
	if utf8Locale(get) {
		return environ
	}
	return append(append([]string{}, environ...), "LC_CTYPE="+utf8CType())
}

func utf8CType() string {
	if runtime.GOOS == "darwin" {
		// macOS has no C.UTF-8; "UTF-8" is its character type of that name.
		return "UTF-8"
	}
	return "C.UTF-8"
}

// utf8Locale is the check tmux makes: the first of LC_ALL, LC_CTYPE, LANG
// that is set, naming UTF-8.
func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToUpper(v)
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return false
}

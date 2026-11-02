package plugin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repo is where a bare name installs from: <Repo>/<name>/plugin.json.
// TUI_PLUGINS_REPO points it at a fork.
func Repo() string {
	if r := os.Getenv("TUI_PLUGINS_REPO"); r != "" {
		return strings.TrimSuffix(r, "/")
	}
	return "https://raw.githubusercontent.com/sophonz-labs/tui-plugins/main/plugins"
}

// Main is "<tool> ext ...": list, install, remove.
func Main(tool string, b Builtins, args []string, in io.Reader, out io.Writer) int {
	usage := func() int {
		fmt.Fprintf(out, `usage: %[1]s ext list
       %[1]s ext install [-y] <name | path | url>
       %[1]s ext remove <name>

A name installs from %[2]s/<name>/plugin.json.
Plugins live in %[3]s (and %[4]s, shared with other TUIs).
`, tool, Repo(), Dirs(tool)[0], Dirs(tool)[1])
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "list", "ls":
		ps := Load(Dirs(tool))
		Resolve(ps, b)
		if len(ps) == 0 {
			fmt.Fprintf(out, "no plugins in %s or %s\n", Dirs(tool)[0], Dirs(tool)[1])
			return 0
		}
		for _, p := range ps {
			status := "ok"
			if p.Err != nil {
				status = "error: " + strings.ReplaceAll(p.Err.Error(), "\n", "; ")
			} else if len(p.Problems) > 0 {
				status = strings.Join(p.Problems, "; ")
			}
			fmt.Fprintf(out, "%-16s %s\n  %s\n", p.Name, p.Path, status)
		}
		return 0
	case "install", "add":
		yes := false
		rest := args[1:]
		if len(rest) > 0 && (rest[0] == "-y" || rest[0] == "--yes") {
			yes, rest = true, rest[1:]
		}
		if len(rest) != 1 {
			return usage()
		}
		if err := install(tool, rest[0], yes, in, out); err != nil {
			fmt.Fprintf(out, "%s ext install: %v\n", tool, err)
			return 1
		}
		return 0
	case "remove", "rm":
		if len(args) != 2 {
			return usage()
		}
		if err := remove(tool, args[1], out); err != nil {
			fmt.Fprintf(out, "%s ext remove: %v\n", tool, err)
			return 1
		}
		return 0
	}
	return usage()
}

// Fetch is a manifest's bytes from a path (a plugin.json or a directory
// with one), an http(s) URL, or a bare name in Repo.
func Fetch(src string) (data []byte, from string, err error) {
	switch {
	case strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://"):
		from = src
	case strings.ContainsRune(src, os.PathSeparator) || strings.HasSuffix(src, ".json") || src == ".":
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			src = filepath.Join(src, "plugin.json")
		}
		data, err = os.ReadFile(src)
		return data, src, err
	case nameRe.MatchString(src):
		from = Repo() + "/" + src + "/plugin.json"
	default:
		return nil, "", fmt.Errorf("%q is not a plugin name, a path or a URL", src)
	}
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(from)
	if err != nil {
		return nil, from, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, from, fmt.Errorf("%s: %s", from, resp.Status)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, from, err
}

func install(tool, src string, yes bool, in io.Reader, out io.Writer) error {
	data, from, err := Fetch(src)
	if err != nil {
		return err
	}
	m, err := Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", from, err)
	}
	dest := filepath.Join(Dirs(tool)[0], m.Name, "plugin.json")
	fmt.Fprintf(out, "%s from %s\n", m.Name, from)
	if m.Description != "" {
		fmt.Fprintf(out, "  %s\n", m.Description)
	}
	if len(m.Requires) > 0 {
		fmt.Fprintf(out, "  needs: %s\n", strings.Join(m.Requires, " "))
	}
	fmt.Fprintln(out, "\nIt will run these, with {placeholders} filled from the row or pane:")
	for _, l := range Describe(m) {
		fmt.Fprintln(out, "  "+l)
	}
	fmt.Fprintln(out)
	verb := "Install it to"
	if _, err := os.Stat(dest); err == nil {
		verb = "Replace"
	}
	if !yes {
		fmt.Fprintf(out, "%s %s? [y/N] ", verb, dest)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not installed")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s: %s\n", m.Name, dest)
	return nil
}

func remove(tool, name string, out io.Writer) error {
	if !nameRe.MatchString(name) { // never a path: nothing outside the plugin dir goes
		return fmt.Errorf("%q is not a plugin name", name)
	}
	own := filepath.Join(Dirs(tool)[0], name)
	if _, err := os.Stat(filepath.Join(own, "plugin.json")); err != nil {
		if shared := filepath.Join(Dirs(tool)[1], name); exists(shared) {
			return fmt.Errorf("%s is in the shared directory %s; other TUIs read it too, so remove it there by hand", name, shared)
		}
		return fmt.Errorf("no plugin %s in %s", name, Dirs(tool)[0])
	}
	if err := os.RemoveAll(own); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", own)
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Package config is <tool>/config.yaml, k9s style: a missing file is the
// defaults, an unknown key or a bad value is a warning and the default,
// never an error. tmui and stui share it; the views a tool can start on
// are its own.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the settings in effect.
type Config struct {
	Logo            bool          // ui.logo
	Headless        bool          // ui.headless: no header at all
	Color           bool          // ui.color: false is NO_COLOR
	RefreshRate     time.Duration // refreshRate
	StartView       string        // startView
	PluginsEnabled  bool          // plugins.enabled
	PluginsDisabled []string      // plugins.disabled

	Path     string
	ModTime  time.Time         // of the file read, zero when there was none
	Source   map[string]string // key: "default", "file", or why else
	Warnings []string
}

// Keys is every key, in the order show lists them.
var Keys = []string{"ui.logo", "ui.headless", "ui.color", "refreshRate", "startView", "plugins.enabled", "plugins.disabled"}

// MinRefresh is the shortest refreshRate: each refresh runs tmux.
const MinRefresh = 500 * time.Millisecond

// Path is $XDG_CONFIG_HOME/<tool>/config.yaml (default ~/.config).
func Path(tool string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, tool, "config.yaml")
}

// Defaults is the settings without a file; views[0] is the start view.
func Defaults(views []string) *Config {
	c := &Config{Logo: true, Color: true, RefreshRate: 2 * time.Second, StartView: views[0], PluginsEnabled: true, Source: map[string]string{}}
	for _, k := range Keys {
		c.Source[k] = "default"
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		c.Color, c.Source["ui.color"] = false, "NO_COLOR"
	}
	return c
}

// Load reads path over the defaults. It never fails: what it cannot use
// is a warning.
func Load(path string, views []string) *Config {
	c := Defaults(views)
	c.Path = path
	st, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			c.Warnings = append(c.Warnings, err.Error())
		}
		return c
	}
	c.ModTime = st.ModTime()
	data, err := os.ReadFile(path)
	if err != nil {
		c.Warnings = append(c.Warnings, err.Error())
		return c
	}
	c.parse(data, views)
	return c
}

func (c *Config) warn(n *yaml.Node, f string, a ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf("config.yaml:%d: ", n.Line)+fmt.Sprintf(f, a...))
}

func (c *Config) parse(data []byte, views []string) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		c.Warnings = append(c.Warnings, "config.yaml: "+err.Error()+"; using the defaults")
		return
	}
	if len(doc.Content) == 0 {
		return // an empty file, or only comments
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		c.warn(root, "not a mapping of keys; using the defaults")
		return
	}
	boolean := func(key string, v *yaml.Node, dst *bool) {
		var b bool
		if v.Kind != yaml.ScalarNode || v.Decode(&b) != nil {
			c.warn(v, "%s: %q is not true or false; using %v", key, v.Value, *dst)
			return
		}
		if key == "ui.color" && b && c.Source[key] == "NO_COLOR" {
			c.warn(v, "ui.color: true, but NO_COLOR is set; no colour")
			return
		}
		*dst, c.Source[key] = b, "file"
	}
	section := func(name string, v *yaml.Node, keys map[string]*bool, extra func(k string, v *yaml.Node) bool) {
		if v.Kind != yaml.MappingNode {
			c.warn(v, "%s: not a mapping; using the defaults", name)
			return
		}
		for i := 0; i+1 < len(v.Content); i += 2 {
			k, val := v.Content[i].Value, v.Content[i+1]
			if dst, ok := keys[k]; ok {
				boolean(name+"."+k, val, dst)
			} else if extra == nil || !extra(k, val) {
				c.warn(v.Content[i], "unknown key %s.%s", name, k)
			}
		}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		switch k {
		case "ui":
			section("ui", v, map[string]*bool{"logo": &c.Logo, "headless": &c.Headless, "color": &c.Color}, nil)
		case "plugins":
			section("plugins", v, map[string]*bool{"enabled": &c.PluginsEnabled}, func(k string, v *yaml.Node) bool {
				if k != "disabled" {
					return false
				}
				var names []string
				if v.Decode(&names) != nil {
					c.warn(v, "plugins.disabled: not a list of names; using none")
					return true
				}
				c.PluginsDisabled, c.Source["plugins.disabled"] = names, "file"
				return true
			})
		case "refreshRate":
			d, err := time.ParseDuration(v.Value)
			if v.Kind != yaml.ScalarNode || err != nil || d < MinRefresh {
				c.warn(v, "refreshRate: %q is not a duration of %v or more (2s, 500ms); using %v", v.Value, MinRefresh, c.RefreshRate)
				continue
			}
			c.RefreshRate, c.Source["refreshRate"] = d, "file"
		case "startView":
			if v.Kind != yaml.ScalarNode || !contains(views, v.Value) {
				c.warn(v, "startView: %q is not one of %s; using %s", v.Value, strings.Join(views, " "), c.StartView)
				continue
			}
			c.StartView, c.Source["startView"] = v.Value, "file"
		default:
			c.warn(root.Content[i], "unknown key %s", k)
		}
	}
}

// Value is a key's value as show prints it.
func (c *Config) Value(key string) string {
	switch key {
	case "ui.logo":
		return fmt.Sprint(c.Logo)
	case "ui.headless":
		return fmt.Sprint(c.Headless)
	case "ui.color":
		return fmt.Sprint(c.Color)
	case "refreshRate":
		return c.RefreshRate.String()
	case "startView":
		return c.StartView
	case "plugins.enabled":
		return fmt.Sprint(c.PluginsEnabled)
	case "plugins.disabled":
		return "[" + strings.Join(c.PluginsDisabled, ", ") + "]"
	}
	return ""
}

// Template is the file edit starts from: every key at its default,
// commented out, with what it does.
func Template(tool string, views []string) string {
	return fmt.Sprintf(`# %[1]s's settings. Every key is optional; these are the defaults.
# %[1]s reads this file again when it changes (and on ctrl-r).

# ui:
#   logo: true          # the logo at the header's top right
#   headless: false     # no header at all (ctrl-e toggles it)
#   color: true         # false: no colour, as NO_COLOR (which always wins)

# refreshRate: 2s       # how often %[1]s reads tmux again; 500ms or more

# startView: %[2]s     # the view it opens on: %[3]s

# plugins:
#   enabled: true       # false: load no plugins (%[1]s ext)
#   disabled: []        # plugins to leave out, by name: [lazygit]
`, tool, views[0], strings.Join(views, ", "))
}

// Main is "<tool> config ...": path, show, edit.
func Main(tool string, views []string, args []string, out io.Writer) int {
	path := Path(tool)
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "path":
		fmt.Fprintln(out, path)
	case "show", "":
		c := Load(path, views)
		state := "not there: the defaults"
		if !c.ModTime.IsZero() {
			state = "read"
		}
		fmt.Fprintf(out, "# %s (%s)\n", path, state)
		for _, k := range Keys {
			fmt.Fprintf(out, "%-18s %-12s %s\n", k, c.Value(k), c.Source[k])
		}
		for _, w := range c.Warnings {
			fmt.Fprintln(out, "warning: "+w)
		}
	case "edit":
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				fmt.Fprintln(out, err)
				return 1
			}
			if err := os.WriteFile(path, []byte(Template(tool, views)), 0o644); err != nil {
				fmt.Fprintln(out, err)
				return 1
			}
		}
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		c := editorCmd(editor, path)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			fmt.Fprintf(out, "%s config edit: %v\n", tool, err)
			return 1
		}
		for _, w := range Load(path, views).Warnings {
			fmt.Fprintln(out, "warning: "+w)
		}
	default:
		fmt.Fprintf(out, "usage: %[1]s config [show]   the settings in effect, and where each comes from\n       %[1]s config path   where the file is\n       %[1]s config edit   open it in $EDITOR (made with the defaults, commented, if missing)\n", tool)
		return 2
	}
	return 0
}

// Disabled is whether plugin name is left out.
func (c *Config) Disabled(name string) bool { return contains(c.PluginsDisabled, name) }

// Changed is whether the file at c.Path is not the one c read.
func (c *Config) Changed() bool {
	st, err := os.Stat(c.Path)
	if err != nil {
		return !c.ModTime.IsZero()
	}
	return !st.ModTime().Equal(c.ModTime)
}

// Same is whether two configs set the same things (for saying "read again").
func (c *Config) Same(o *Config) bool {
	for _, k := range Keys {
		if c.Value(k) != o.Value(k) {
			return false
		}
	}
	a, b := append([]string(nil), c.Warnings...), append([]string(nil), o.Warnings...)
	sort.Strings(a)
	sort.Strings(b)
	return bytes.Equal([]byte(strings.Join(a, "\n")), []byte(strings.Join(b, "\n")))
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// editorCmd opens path in editor. $EDITOR may carry flags ("code -w"), as git
// takes it, so a shell splits it; Windows has no sh and gets notepad.
func editorCmd(editor, path string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if editor == "" {
			editor = "notepad"
		}
		return exec.Command(editor, path)
	}
	if editor == "" {
		editor = "vi"
	}
	return exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
}

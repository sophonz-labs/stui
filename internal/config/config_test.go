package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var views = []string{"sessions", "agents", "orch", "ci", "usage"}

func load(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p, views)
}

func TestDefaults(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	c := Load(filepath.Join(t.TempDir(), "none.yaml"), views)
	if !c.Logo || c.Headless || !c.Color || c.RefreshRate != 2*time.Second || c.StartView != "sessions" || !c.PluginsEnabled || !c.STRead || len(c.Warnings) != 0 {
		t.Errorf("defaults: %+v", c)
	}
	if c.Source["ui.logo"] != "default" {
		t.Errorf("source %q", c.Source["ui.logo"])
	}
}

func TestFile(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := load(t, "ui:\n  logo: false\n  headless: true\nrefreshRate: 5s\nstartView: orch\nplugins:\n  disabled: [lazygit, gh-prs]\nst:\n  read: false\n")
	if c.STRead || c.Logo || !c.Headless || c.RefreshRate != 5*time.Second || c.StartView != "orch" || !c.Disabled("lazygit") || c.Disabled("git-status") || len(c.Warnings) != 0 {
		t.Errorf("file: %+v", c)
	}
	if c.Source["ui.logo"] != "file" || c.Source["ui.color"] != "default" {
		t.Errorf("sources: %v", c.Source)
	}
}

func TestWarnings(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := load(t, "ui:\n  logo: maybe\n  skin: dark\nrefreshRate: 10ms\nstartView: nodes\nzoom: 2\nplugins:\n  disabled: lazygit\n")
	if !c.Logo || c.RefreshRate != 2*time.Second || c.StartView != "sessions" {
		t.Errorf("bad values must leave the defaults: %+v", c)
	}
	all := strings.Join(c.Warnings, "\n")
	for _, want := range []string{":2: ui.logo", ":3: unknown key ui.skin", ":4: refreshRate", ":5: startView: \"nodes\"", ":6: unknown key zoom", "plugins.disabled"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in\n%s", want, all)
		}
	}
	if c := load(t, "ui: [\n"); len(c.Warnings) != 1 || !c.Logo {
		t.Errorf("broken yaml: %+v", c)
	}
	if c := load(t, "# only comments\n"); len(c.Warnings) != 0 {
		t.Errorf("comments: %v", c.Warnings)
	}
}

func TestNoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	c := load(t, "ui:\n  color: true\n")
	if c.Color || c.Source["ui.color"] != "NO_COLOR" || len(c.Warnings) != 1 {
		t.Errorf("NO_COLOR: %+v", c)
	}
}

// The file edit starts from reads as the defaults, warning-free.
func TestTemplate(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	tpl := Template("stui", views)
	c := load(t, tpl)
	if len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("template: %v", c.Warnings)
	}
	// Uncommented, it is still the defaults.
	var b strings.Builder
	for _, l := range strings.Split(tpl, "\n") {
		if strings.HasPrefix(l, "# ") && strings.Contains(l, ":") && !strings.Contains(l, "settings") && !strings.Contains(l, "reads this") {
			l = l[2:]
		}
		b.WriteString(l + "\n")
	}
	if c := load(t, b.String()); len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("uncommented template: %v\n%s", c.Warnings, b.String())
	}
}

func TestChanged(t *testing.T) {
	c := load(t, "ui:\n  logo: false\n")
	if c.Changed() {
		t.Error("changed right after reading")
	}
	later := c.ModTime.Add(time.Second)
	os.Chtimes(c.Path, later, later)
	if !c.Changed() {
		t.Error("not changed after a write")
	}
}

// The repository's config.example.yaml is the defaults, warning-free.
func TestExample(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	c := Load("../../config.example.yaml", views)
	if c.ModTime.IsZero() || len(c.Warnings) != 0 || !c.Same(Defaults(views)) {
		t.Errorf("config.example.yaml: %v", c.Warnings)
	}
}

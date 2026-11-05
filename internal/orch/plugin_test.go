package orch

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRefreshInstalledSkills(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	var calls []string
	run := func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "stui@stui", nil
	}
	if changed, err := RefreshPlugin("v1", run); changed || err != nil || len(calls) != 0 {
		t.Fatalf("uninstalled: %v %v %v", changed, err, calls)
	}
	if _, err := os.Stat(PluginRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created plugin without opt-in")
	}
	if _, err := os.Stat(filepath.Join(CodexSkillsDir(), "stui-orchestrate")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created skills without opt-in")
	}

	if _, err := WriteCodexSkills(); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(CodexSkillsDir(), "stui-orchestrate", "SKILL.md")
	original, _ := os.ReadFile(skill)
	if err := os.WriteFile(skill, []byte("outdated"), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err := RefreshPlugin("v1", run); !changed || err != nil || len(calls) != 0 {
		t.Fatalf("Codex only: %v %v %v", changed, err, calls)
	}
	if b, _ := os.ReadFile(skill); string(b) != string(original) {
		t.Fatal("Codex-only repair failed")
	}
	if _, err := os.Stat(PluginRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Codex-only repair installed Claude")
	}

	root, err := WritePlugin("v1")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := RefreshPlugin("v1", run); !changed || err != nil {
		t.Fatalf("initial cache refresh: %v %v", changed, err)
	}
	want := "plugin list|plugin marketplace update stui|plugin update stui@stui --scope user"
	if strings.Join(calls, "|") != want {
		t.Fatalf("Claude calls: %v", calls)
	}
	calls = nil
	if changed, err := RefreshPlugin("v1", run); changed || err != nil || len(calls) != 0 {
		t.Fatalf("current: %v %v %v", changed, err, calls)
	}

	// A stale template with an unchanged version must refresh the cached copy too.
	path := filepath.Join(root, "stui/skills/orchestrate/SKILL.md")
	if err := os.WriteFile(path, []byte("old same version"), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err := RefreshPlugin("v1", run); !changed || err != nil || strings.Join(calls, "|") != want {
		t.Fatalf("same-version repair: %v %v %v", changed, err, calls)
	}

	// Uninstall only removes the two owned skill directories.
	other := filepath.Join(CodexSkillsDir(), "other", "SKILL.md")
	os.MkdirAll(filepath.Dir(other), 0755)
	os.WriteFile(other, []byte("keep"), 0644)
	RemoveCodexSkills()
	calls = nil
	if _, err := RefreshPlugin("v2", run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(skill); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reinstalled removed Codex skills")
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Fatal("changed unrelated skills")
	}
}

func TestRefreshFailuresAndClaudeRegistration(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := WritePlugin("v1"); err != nil {
		t.Fatal(err)
	}
	fail := true
	var updates int
	run := func(args ...string) (string, error) {
		if args[1] == "list" {
			return "stui@stui", nil
		}
		if args[1] == "update" {
			updates++
			if fail {
				return "cache unavailable", errors.New("update failed")
			}
		}
		return "", nil
	}
	if _, err := RefreshPlugin("v2", run); err == nil {
		t.Fatal("lost update error")
	}
	fail = false
	if _, err := RefreshPlugin("v2", run); err != nil {
		t.Fatal(err)
	}
	if updates != 2 {
		t.Fatalf("did not retry failed cache update: %d", updates)
	}

	var calls []string
	absent := func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "other@other", nil
	}
	if _, err := RefreshPlugin("v3", absent); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, "|") != "plugin list" {
		t.Fatalf("registered absent plugin: %v", calls)
	}

	// A broken Codex destination must be reported; it is not successful refresh.
	if _, err := WriteCodexSkills(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(CodexSkillsDir(), "stui-worker", "SKILL.md")
	os.Remove(p)
	os.Mkdir(p, 0755)
	if _, err := RefreshPlugin("v3", nil); err == nil {
		t.Fatal("lost Codex write/read error")
	}
}

// The generated entrypoints must invoke this product and resolve every guide
// reference. This catches a stale copy that calls st instead of stui.
func TestGeneratedSkillGuideCommands(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	root, err := WritePlugin("test")
	if err != nil {
		t.Fatal(err)
	}
	codex, err := WriteCodexSkills()
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`\b(stui|st) skills get ([a-z]+)`)
	for _, path := range []string{
		filepath.Join(root, "stui/skills/orchestrate/SKILL.md"),
		filepath.Join(root, "stui/skills/worker/SKILL.md"),
		filepath.Join(codex, "stui-orchestrate/SKILL.md"),
		filepath.Join(codex, "stui-worker/SKILL.md"),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		matches := refs.FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			t.Fatalf("no executable guide reference in %s", path)
		}
		for _, m := range matches {
			if m[1] != "stui" {
				t.Fatalf("%s invokes another product: %s", path, m[0])
			}
			if _, err := Guide(m[2]); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
	}
}

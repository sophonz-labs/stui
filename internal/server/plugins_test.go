package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sophonz-labs/stui/internal/orch"
)

func TestDevelopmentBuildKeepsInstalledSkills(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	d, err := orch.WriteCodexSkills()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "stui-orchestrate", "SKILL.md")
	if err := os.WriteFile(path, []byte("installed version"), 0644); err != nil {
		t.Fatal(err)
	}
	old := Version
	Version = "dev"
	defer func() { Version = old }()
	refreshSkills(context.Background())
	if b, err := os.ReadFile(path); err != nil || string(b) != "installed version" {
		t.Fatalf("source build changed installed skill: %q %v", b, err)
	}
}

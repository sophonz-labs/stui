package vault

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeepListReadRemove(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(src, []byte("intro\n# The plan\n\nsteps"), 0o644)
	p, err := Keep(src, "main", "build/x")
	if err != nil || p != "main/build-x/plan.md" {
		t.Fatalf("keep: %q %v", p, err)
	}
	// The same text again is the same document; another gets a number.
	if again, _ := Keep(src, "main", "build/x"); again != p {
		t.Errorf("same text kept as %q", again)
	}
	os.WriteFile(src, []byte("other"), 0o644)
	if other, _ := Keep(src, "main", "build/x"); other != "main/build-x/plan (2).md" {
		t.Errorf("other text kept as %q", other)
	}
	if top, _ := Keep(src, "", ".."); top != "plan.md" {
		t.Errorf("outside a pane: %q", top)
	}
	docs, err := List()
	if err != nil || len(docs) != 3 || docs[0].Path != "main/build-x/plan (2).md" || docs[1].Title != "The plan" || docs[0].Title != "plan (2)" {
		t.Fatalf("list: %+v %v", docs, err)
	}
	if b, err := Read(p); err != nil || string(b) != "intro\n# The plan\n\nsteps" {
		t.Errorf("read: %q %v", b, err)
	}
	for _, bad := range []string{"", "..", "../x.md", "/etc/passwd", "a/../../x"} {
		if _, err := Read(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	// Removing the last document of a folder takes the folder too.
	Remove(p)
	Remove("main/build-x/plan (2).md")
	if _, err := os.Stat(filepath.Join(Dir(), "main")); !os.IsNotExist(err) {
		t.Errorf("empty folder left: %v", err)
	}
	if docs, _ := List(); len(docs) != 1 {
		t.Errorf("after remove: %+v", docs)
	}
}

// No vault yet: nothing, not an error.
func TestListEmpty(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	if docs, err := List(); err != nil || len(docs) != 0 {
		t.Errorf("%v %v", docs, err)
	}
}

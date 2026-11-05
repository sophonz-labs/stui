package cli

import (
	"strings"
	"testing"

	"github.com/sophonz-labs/stui/internal/orch"
)

func TestSkillEntryByRole(t *testing.T) {
	common, err := orch.Guide("common")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := orch.Guide("worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUI_RUN", "")
	if got := skillHere(); got != common {
		t.Fatal("ordinary stui task did not get the common entrypoint")
	}
	t.Setenv("STUI_RUN", "r-test")
	if got := skillHere(); got != common+"\n"+worker {
		t.Fatal("worker lost common instructions or reporting obligations")
	}
	for _, name := range orch.GuideNames() {
		g, err := orch.Guide(name)
		if err != nil || strings.TrimSpace(g) == "" {
			t.Fatalf("unreadable topic %s: %v", name, err)
		}
	}
}

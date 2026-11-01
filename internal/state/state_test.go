package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionRoundTrip(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	in := &Agent{PID: 10, Port: 7911, Token: "t", Session: "s", Version: "0.3.15"}
	if err := Write(in); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file())
	if !strings.Contains(string(b), `"version": "0.3.15"`) {
		t.Errorf("version not written: %s", b)
	}
	out := Read()
	if out == nil || *out != *in {
		t.Fatalf("read back %+v, want %+v", out, in)
	}
}

// A record from a build that kept no version reads as having none.
func TestVersionMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STUI_HOME", dir)
	os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"pid":10,"port":7911,"token":"t","session":"s","startedAt":1}`), 0o644)
	a := Read()
	if a == nil || a.Version != "" || a.Port != 7911 {
		t.Fatalf("%+v", a)
	}
}

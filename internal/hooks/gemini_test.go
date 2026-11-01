package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func geminiHooksOf(t *testing.T, e Env) map[string][]struct {
	Hooks []struct {
		Type    string      `json:"type"`
		Command string      `json:"command"`
		Timeout json.Number `json:"timeout"`
	} `json:"hooks"`
} {
	t.Helper()
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string      `json:"type"`
				Command string      `json:"command"`
				Timeout json.Number `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, e.GeminiSettings())), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Hooks
}

func TestInstallGemini(t *testing.T) {
	e := testEnv(t)
	if _, err := InstallGemini(e); err != nil {
		t.Fatal(err)
	}
	got := geminiHooksOf(t, e)
	if len(got) != len(GeminiEvents) {
		t.Fatalf("%d events, want %d", len(got), len(GeminiEvents))
	}
	for _, ev := range GeminiEvents {
		groups, ok := got[ev]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", ev, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "/usr/local/bin/stui hook gemini --hv 1" {
			t.Errorf("%s: %+v", ev, h)
		}
		// Gemini CLI reads timeout in MILLISECONDS (its default is 60000),
		// where Claude Code reads seconds. Ten would kill the hook before it
		// could connect.
		if h.Timeout.String() != "10000" {
			t.Errorf("%s: timeout %s, want 10000 (milliseconds)", ev, h.Timeout)
		}
		if ok, v := hookVersion(h.Command); !ok || v != GeminiVersion {
			t.Errorf("%s: stui does not recognise its own command (%v, %d)", ev, ok, v)
		}
	}
	// Installing again changes nothing and adds nothing.
	before := read(t, e.GeminiSettings())
	if _, err := InstallGemini(e); err != nil {
		t.Fatal(err)
	}
	if after := read(t, e.GeminiSettings()); after != before {
		t.Errorf("a second install rewrote the file:\n%s", after)
	}
}

// Everything that is not stui's is left exactly as it was, keys in order.
func TestInstallGeminiKeepsTheUsersFile(t *testing.T) {
	e := testEnv(t)
	write(t, e.GeminiSettings(), `{
  "theme": "GitHub",
  "hooks": {
    "BeforeAgent": [
      {
        "matcher": "*",
        "hooks": [
          { "type": "command", "name": "mine", "command": "~/bin/note.sh" }
        ]
      }
    ],
    "PreCompress": [
      { "hooks": [ { "type": "command", "command": "~/bin/compress.sh" } ] }
    ]
  },
  "selectedAuthType": "oauth-personal"
}`)
	if _, err := InstallGemini(e); err != nil {
		t.Fatal(err)
	}
	out := read(t, e.GeminiSettings())
	for _, keep := range []string{`"theme": "GitHub"`, `"name": "mine"`, "~/bin/note.sh",
		"~/bin/compress.sh", `"selectedAuthType": "oauth-personal"`, `"matcher": "*"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %s from:\n%s", keep, out)
		}
	}
	if i, j := strings.Index(out, `"theme"`), strings.Index(out, `"selectedAuthType"`); i > j {
		t.Error("keys were reshuffled")
	}
	// An event stui does not listen to is untouched.
	got := geminiHooksOf(t, e)
	if len(got["PreCompress"]) != 1 || len(got["PreCompress"][0].Hooks) != 1 {
		t.Errorf("PreCompress: %+v", got["PreCompress"])
	}
	// The user's BeforeAgent hook and stui's live side by side.
	var user, ours int
	for _, g := range got["BeforeAgent"] {
		for _, h := range g.Hooks {
			if ok, _ := hookVersion(h.Command); ok {
				ours++
			} else {
				user++
			}
		}
	}
	if user != 1 || ours != 1 {
		t.Errorf("BeforeAgent: %d of the user's, %d of stui's", user, ours)
	}
}

func TestUninstallGemini(t *testing.T) {
	e := testEnv(t)
	// Nothing there at all is not an error.
	if _, err := UninstallGemini(e); err != nil {
		t.Fatal(err)
	}
	write(t, e.GeminiSettings(), `{"hooks":{"AfterAgent":[{"hooks":[{"type":"command","command":"~/bin/mine.sh"}]}]},"theme":"X"}`)
	if _, err := InstallGemini(e); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallGemini(e); err != nil {
		t.Fatal(err)
	}
	out := read(t, e.GeminiSettings())
	if strings.Contains(out, "hook gemini") {
		t.Errorf("stui's hooks survived:\n%s", out)
	}
	if !strings.Contains(out, "~/bin/mine.sh") || !strings.Contains(out, `"theme"`) {
		t.Errorf("took the user's with it:\n%s", out)
	}
	// Ours alone leaves no empty hooks object behind.
	e2 := testEnv(t)
	InstallGemini(e2)
	UninstallGemini(e2)
	if o := read(t, e2.GeminiSettings()); strings.Contains(o, `"hooks"`) {
		t.Errorf("empty hooks object left:\n%s", o)
	}
}

func TestGeminiInfo(t *testing.T) {
	e := testEnv(t)
	info, err := GeminiInfo(e)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != "none" || info.Required != GeminiVersion || len(info.Missing) != len(GeminiEvents) {
		t.Fatalf("empty: %+v", info)
	}
	InstallGemini(e)
	if info, _ = GeminiInfo(e); info.State != "installed" || info.Version != GeminiVersion || len(info.Missing) != 0 {
		t.Fatalf("installed: %+v", info)
	}
	// A set with an event taken out is partial, not installed.
	out := read(t, e.GeminiSettings())
	var doc map[string]any
	json.Unmarshal([]byte(out), &doc)
	delete(doc["hooks"].(map[string]any), "AfterAgent")
	b, _ := json.Marshal(doc)
	write(t, e.GeminiSettings(), string(b))
	if info, _ = GeminiInfo(e); info.State != "partial" || len(info.Missing) != 1 || info.Missing[0] != "AfterAgent" {
		t.Fatalf("partial: %+v", info)
	}
}

// stui hooks install offers Gemini now, and does not explain why it cannot.
func TestGeminiIsHookable(t *testing.T) {
	a, ok := Find("gemini")
	if !ok {
		t.Fatal("no gemini")
	}
	if a.Install == nil || a.Uninstall == nil || a.Info == nil || a.Status == nil {
		t.Fatal("gemini is not wired for install")
	}
	if a.Why != "" {
		t.Errorf("still says it cannot: %q", a.Why)
	}
	e := testEnv(t)
	if s := a.Status(e); s != NotInstalled {
		t.Errorf("status before install: %v", s)
	}
	if _, err := a.Install(e); err != nil {
		t.Fatal(err)
	}
	if s := a.Status(e); s != Installed {
		t.Errorf("status after install: %v", s)
	}
}

// The events stui asks for are ones Gemini CLI documents, and the ones agent
// watch already knows how to read.
func TestGeminiEventsAreTheDocumentedOnes(t *testing.T) {
	documented := map[string]bool{
		"SessionStart": true, "SessionEnd": true, "BeforeAgent": true, "AfterAgent": true,
		"BeforeModel": true, "AfterModel": true, "BeforeToolSelection": true,
		"BeforeTool": true, "AfterTool": true, "Notification": true, "PreCompress": true,
	}
	for _, ev := range GeminiEvents {
		if !documented[ev] {
			t.Errorf("%s is not an event Gemini CLI documents", ev)
		}
	}
}

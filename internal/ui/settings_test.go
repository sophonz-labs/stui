package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/config"
)

func settingsApp(w, h int) *app {
	a := &app{snap: &snap{}, folded: map[string]bool{}, ver: "v0.1.0", w: w, h: h, stack: []*page{{v: sessionsView{}}}}
	a.applyConfig(config.Defaults(startViews))
	return a
}

func TestHeaderFollowsConfig(t *testing.T) {
	a := settingsApp(200, 30)
	if a.headerRows() != 9 {
		t.Fatalf("200x30: %d header rows, want the logo's 9", a.headerRows())
	}
	a.h = logoTall
	if a.headerRows() != 9 {
		t.Errorf("200x%d: %d rows", logoTall, a.headerRows())
	}
	a.h = logoTall - 1
	if a.headerRows() != 6 {
		t.Errorf("200x%d: %d rows", logoTall-1, a.headerRows())
	}
	a.h, a.w = 30, 120
	if a.headerRows() != 6 || !strings.HasSuffix(strings.TrimRight(plain(a.render()[0]), " "), logoWord) {
		t.Errorf("120 columns: the one-row name, %d rows", a.headerRows())
	}
	a.w = 200
	a.runCommand("logo")
	if top := strings.TrimRight(plain(a.render()[0]), " "); a.headerRows() != 6 || strings.HasSuffix(top, logoWord) || strings.HasSuffix(top, logoRows[0]) {
		t.Errorf(":logo off still draws it: %q", top)
	}
	a.normalKey("ctrl-e")
	if a.headerRows() != 0 || len(a.render()) != 30 {
		t.Errorf("ctrl-e: %d header rows", a.headerRows())
	}
	rows := configView{}.rows(a)
	if rows[0].cells[2] != "this session (:logo)" || rows[1].cells[2] != "this session (ctrl-e)" {
		t.Errorf(":config sources: %v %v", rows[0].cells, rows[1].cells)
	}
}

func TestConfigReload(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	a := settingsApp(120, 30)
	a.applyConfig(config.Load(path, startViews))
	if !a.logoOn {
		t.Fatal("no file: logo on")
	}
	os.WriteFile(path, []byte("ui:\n  logo: false\n  shiny: true\nrefreshRate: 750ms\nstartView: orch\n"), 0o644)
	a.reloadConfig()
	if a.logoOn || time.Duration(a.rate.Load()) != 750*time.Millisecond || a.cfg.StartView != "orch" {
		t.Errorf("reload: logo %v rate %v start %q", a.logoOn, time.Duration(a.rate.Load()), a.cfg.StartView)
	}
	if !a.bad || !strings.Contains(a.flash, "unknown key ui.shiny") {
		t.Errorf("warning not said: %q", a.flash)
	}
	rows := configView{}.rows(a)
	if last := rows[len(rows)-1]; last.cells[0] != "warning" {
		t.Errorf(":config has no warning row: %v", last.cells)
	}
}

func TestMonochrome(t *testing.T) {
	line := cAqua + bold + "name" + reset + cTeal + "agent" + reset + selected + "row" + reset + crumbOn + " <x> " + reset
	got := monochrome(line)
	if strings.Contains(got, "38;") || strings.Contains(got, "48;") || !strings.Contains(got, "\x1b[7mrow") || !strings.Contains(got, bold+"name") {
		t.Errorf("%q", got)
	}
}

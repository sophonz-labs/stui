package ui

import (
	"path"
	"sort"
	"strings"

	"github.com/sophonz-labs/stui/internal/vault"
	"github.com/sophonz-labs/tmui/pkg/termmd"
)

// The docs view (7, :docs): what stui doc kept on this machine
// (internal/vault), newest first; enter reads one.

type docsView struct{}

func (docsView) title() string    { return "Docs" }
func (docsView) scope() string    { return "~/.stui/vault" }
func (docsView) header() []string { return []string{"TITLE", "FOLDER", "CHANGED"} }
func (docsView) rows(a *app) []row {
	ds := append([]vault.Doc(nil), a.snap.docs...)
	sortDocs(ds)
	var out []row
	for _, d := range ds {
		folder := path.Dir(d.Path)
		if folder == "." {
			folder = ""
		}
		out = append(out, row{id: d.Path, color: cWhite, cells: []string{d.Title, folder, ago(d.Mod.UnixMilli()) + " ago"}})
	}
	return out
}
func (docsView) hints() []hint { return []hint{{"enter", "read"}} }
func (docsView) key(a *app, k string, r *row) bool {
	if k != "enter" || r == nil {
		return false
	}
	b, err := vault.Read(r.id)
	if err != nil {
		a.say(err.Error(), true)
		return true
	}
	// The pager draws plain text, so no colour (the workspace's Docs tab has it).
	text := termmd.Render(string(b), termmd.Options{Width: max(20, min(a.w-4, 100))})
	a.pushPager(&pager{title: r.cells[0], scope: r.id, lines: strings.Split(strings.TrimRight(text, "\n"), "\n")})
	return true
}

// sortDocs puts the newest first.
func sortDocs(ds []vault.Doc) {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Mod.After(ds[j].Mod) })
}

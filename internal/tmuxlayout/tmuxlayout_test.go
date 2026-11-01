package tmuxlayout

import "testing"

// Measured: tmux 3.8, -f /dev/null, 80x24, three panes (the same before and
// after zooming pane 0).
const v38 = `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":40,"h":24,"x":0,"y":0,"l":1,"i":0,"I":"%0"},{"t":"v","w":39,"h":24,"x":41,"y":0,"c":[{"t":"p","w":39,"h":12,"x":41,"y":0,"l":0,"i":1,"I":"%1"},{"t":"p","w":39,"h":11,"x":41,"y":13,"a":true,"i":2,"I":"%2"}]}]}}`

// The same window in the older form.
const old = "0000,80x24,0,0{40x24,0,0,0,39x24,41,0[39x12,41,0,1,39x11,41,13,2]}"

func TestBothForms(t *testing.T) {
	want := map[string]Box{"%0": {0, 0, 40, 24}, "%1": {41, 0, 39, 12}, "%2": {41, 13, 39, 11}}
	for _, l := range []string{v38, old} {
		if w, h := Size(l); w != 80 || h != 24 {
			t.Errorf("Size(%.10s…) = %d, %d", l, w, h)
		}
		for id, b := range want {
			p, win, ok := Pane(l, id)
			if !ok || p != b || win.W != 80 || win.H != 24 {
				t.Errorf("Pane(%.10s…, %s) = %+v %+v %v", l, id, p, win, ok)
			}
		}
		if _, _, ok := Pane(l, "%9"); ok {
			t.Errorf("%.10s…: a pane that is not there", l)
		}
		if _, _, ok := Pane(l, "0"); ok {
			t.Errorf("%.10s…: an id without %%", l)
		}
	}
	for _, bad := range []string{"{", `{"V":2}`, `{"V":2,"L":{"t":"p","w":0,"h":0}}`, "[]"} {
		if w, h := Size(bad); w != 0 || h != 0 {
			t.Errorf("Size(%q) = %d, %d", bad, w, h)
		}
		if _, _, ok := Pane(bad, "%0"); ok {
			t.Errorf("Pane(%q)", bad)
		}
	}
}

// Package tmuxlayout reads tmux's window_layout. It comes in two forms, told
// apart by its first character:
//
//   - "b25d,161x40,0,0{80x40,0,0,1,80x40,81,0[...]}": a checksum, then
//     WxH,X,Y and a pane's number or a split in {} (side by side) or []
//     (stacked). tmux before 3.8, and psmux.
//   - {"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p",...,"I":"%0"},...]}}:
//     JSON, from tmux 3.8 (measured, 3.8 on macOS). "t" is p
//     (pane), h or v (a split), "c" the children, "I" a pane's id.
//
// Either way the root's size is the window's, and while the window is
// zoomed it is the layout under the zoom.
package tmuxlayout

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Box is a cell's place and size, in cells.
type Box struct{ X, Y, W, H int }

func isJSON(layout string) bool { return strings.HasPrefix(layout, "{") }

type jsonNode struct {
	T  string `json:"t"`
	W  int    `json:"w"`
	H  int    `json:"h"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	ID string `json:"I"`
	// The pane's index. Named so that "i" is not read into ID: encoding/json
	// matches a key that has no field of its own case-insensitively.
	Index    int        `json:"i"`
	Children []jsonNode `json:"c"`
}

func readJSON(layout string) (jsonNode, bool) {
	var l struct {
		L *jsonNode `json:"L"`
	}
	if json.Unmarshal([]byte(layout), &l) != nil || l.L == nil || l.L.W <= 0 || l.L.H <= 0 {
		return jsonNode{}, false
	}
	return *l.L, true
}

func (n jsonNode) find(id string, depth int) (Box, bool) {
	if depth > 64 {
		return Box{}, false
	}
	if n.T == "p" {
		return Box{n.X, n.Y, n.W, n.H}, n.ID == id
	}
	for _, c := range n.Children {
		if b, ok := c.find(id, depth+1); ok {
			return b, true
		}
	}
	return Box{}, false
}

// Size is the window size the layout was made at; 0, 0 when it cannot be
// read.
func Size(layout string) (w, h int) {
	if isJSON(layout) {
		root, ok := readJSON(layout)
		if !ok {
			return 0, 0
		}
		return root.W, root.H
	}
	_, rest, ok := strings.Cut(layout, ",")
	if !ok {
		return 0, 0
	}
	dims, _, _ := strings.Cut(rest, ",")
	ws, hs, ok := strings.Cut(dims, "x")
	if !ok {
		return 0, 0
	}
	w, _ = strconv.Atoi(ws)
	h, _ = strconv.Atoi(hs)
	return w, h
}

// Pane is pane id's (%N) box and the window's, or false when the layout
// cannot be read or does not hold it.
func Pane(layout, id string) (pane, window Box, ok bool) {
	if !strings.HasPrefix(id, "%") {
		return Box{}, Box{}, false
	}
	if isJSON(layout) {
		root, ok := readJSON(layout)
		if !ok {
			return Box{}, Box{}, false
		}
		b, ok := root.find(id, 0)
		return b, Box{root.X, root.Y, root.W, root.H}, ok
	}
	_, rest, ok := strings.Cut(layout, ",")
	if !ok {
		return Box{}, Box{}, false
	}
	p := layoutParser{s: rest}
	root, ok := p.cell(id[1:], 0)
	if !ok || p.i != len(p.s) || root.w == 0 || root.h == 0 || p.found == nil {
		return Box{}, Box{}, false
	}
	f := p.found
	return Box{f.x, f.y, f.w, f.h}, Box{root.x, root.y, root.w, root.h}, true
}

type layoutBox struct{ w, h, x, y int }

type layoutParser struct {
	s     string
	i     int
	found *layoutBox
}

func (p *layoutParser) num() (int, bool) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if p.i == start || p.i-start > 9 {
		return 0, false
	}
	n, err := strconv.Atoi(p.s[start:p.i])
	return n, err == nil
}

func (p *layoutParser) eat(c byte) bool {
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

// cell reads "WxH,X,Y" and then ",ID" (a pane) or "{...}" / "[...]" (a split).
func (p *layoutParser) cell(want string, depth int) (layoutBox, bool) {
	var b layoutBox
	var ok bool
	if depth > 64 {
		return b, false
	}
	if b.w, ok = p.num(); !ok || !p.eat('x') {
		return b, false
	}
	if b.h, ok = p.num(); !ok || !p.eat(',') {
		return b, false
	}
	if b.x, ok = p.num(); !ok || !p.eat(',') {
		return b, false
	}
	if b.y, ok = p.num(); !ok {
		return b, false
	}
	for _, open := range []byte{'{', '['} {
		if !p.eat(open) {
			continue
		}
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		for {
			if _, ok := p.cell(want, depth+1); !ok {
				return b, false
			}
			if p.eat(close) {
				return b, true
			}
			if !p.eat(',') {
				return b, false
			}
		}
	}
	start := p.i
	if !p.eat(',') {
		return b, false
	}
	if _, ok := p.num(); !ok {
		return b, false
	}
	if p.s[start+1:p.i] == want {
		found := b
		p.found = &found
	}
	return b, true
}

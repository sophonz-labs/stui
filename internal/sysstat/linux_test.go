//go:build linux

package sysstat

import (
	"os"
	"path/filepath"
	"testing"
)

// A fake /proc: stui (10) with a child (11), and a tmux server (20) with a
// shell (21) running an agent (22) on another thread (task 25).
func fakeProc(t *testing.T, childrenFiles bool) {
	dir := t.TempDir()
	put := func(path, body string) {
		p := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	procs := map[int]struct {
		ppid  string
		pages string
		kids  map[string]string
	}{
		10: {"1", "100", map[string]string{"10": "11 "}},
		11: {"10", "10", map[string]string{"11": ""}},
		20: {"1", "20", map[string]string{"20": "21 "}},
		21: {"20", "5", map[string]string{"21": "", "25": "22 "}},
		22: {"21", "1000", map[string]string{"22": ""}},
		99: {"1", "99999", map[string]string{"99": ""}},
	}
	for pid, p := range procs {
		d := filepath.Join(itoa(pid))
		put(filepath.Join(d, "stat"), itoa(pid)+" (x y) S "+p.ppid+" 0 0 0")
		put(filepath.Join(d, "statm"), "5000 "+p.pages+" 0 0 0 0 0")
		for task, kids := range p.kids {
			if childrenFiles {
				put(filepath.Join(d, "task", task, "children"), kids)
			} else {
				os.MkdirAll(filepath.Join(dir, d, "task", task), 0o755)
			}
		}
	}
	old := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = old })
}

func itoa(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }

func TestLinuxWalk(t *testing.T) {
	page := uint64(os.Getpagesize())
	for _, files := range []bool{true, false} {
		fakeProc(t, files)
		rss, n := Tree(NewWalk(), 10, 20)
		if n != 5 || rss != 1135*page {
			t.Errorf("children files %v: %d pages in %d, want 1135 in 5", files, rss/page, n)
		}
	}
}

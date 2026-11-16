// Package vault keeps documents on this machine: markdown files under
// <stui home>/vault, in a folder per session and window like st's
// (<session>/<window>/<name>.md). stui doc puts them there and the
// workspace's Documents tab reads them. Nothing here is sent anywhere.
package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
)

// Dir is where the documents are.
func Dir() string { return filepath.Join(home.Dir(), "vault") }

// Doc is one document: its path in the vault ("main/build/plan.md"), its
// title (its first heading, else its name) and when it last changed.
type Doc struct {
	Path  string    `json:"path"`
	Title string    `json:"title"`
	Mod   time.Time `json:"modified"`
}

// Keep copies the file at src into the folder of session and window (either
// may be empty: the vault's top) and returns its path in the vault. The same
// text kept again is the same document; another text under the same name
// gets a number: "plan (2).md".
func Keep(src, session, window string) (string, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	folder := ""
	for _, part := range []string{session, window} {
		if part = folderName(part); part != "" {
			folder = filepath.Join(folder, part)
		}
	}
	dir := filepath.Join(Dir(), folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		p := filepath.Join(dir, name)
		old, err := os.ReadFile(p)
		if err == nil && bytes.Equal(old, b) {
			return filepath.ToSlash(filepath.Join(folder, name)), nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(p, b, 0o600); err != nil {
				return "", err
			}
			return filepath.ToSlash(filepath.Join(folder, name)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// folderName is a session's or window's name as one folder: no separators,
// and not "." or "..".
func folderName(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("/", "-", "\\", "-", "\x00", "").Replace(s))
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// List is every markdown document, by path.
func List() ([]Doc, error) {
	root := Dir()
	var out []Doc
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, Doc{Path: filepath.ToSlash(rel), Title: title(p), Mod: info.ModTime()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// title is the file's first "# " heading, else its name without .md.
func title(p string) string {
	b, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(b), "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "# "); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}

// Path is the file of rel, a path in the vault; an error for one that
// leaves it.
func Path(rel string) (string, error) {
	c := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is not a path in the vault", rel)
	}
	return filepath.Join(Dir(), c), nil
}

// Read is the file at rel.
func Read(rel string) ([]byte, error) {
	p, err := Path(rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Remove deletes the document at rel, and its folders when that leaves them
// empty.
func Remove(rel string) error {
	p, err := Path(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	root := Dir()
	for d := filepath.Dir(p); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // not empty
			break
		}
	}
	return nil
}

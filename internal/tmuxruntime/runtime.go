// Package tmuxruntime installs the optional Linux tmux carried by a release.
package tmuxruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Configure selects the optional runtime only with --embedded-tmux (before
// the subcommand), or its inherited marker in a child process.
func Configure(product string, args []string) ([]string, error) {
	key := strings.ToUpper(product) + "_USE_EMBEDDED_TMUX"
	selected := os.Getenv(key) == "1"
	if len(args) > 0 && args[0] == "--embedded-tmux" {
		selected = true
		args = args[1:]
	}
	if !selected {
		return args, nil
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("--embedded-tmux requires a Linux release built with bundled_tmux")
	}
	if err := Prepare(product); err != nil {
		return nil, err
	}
	if err := os.Setenv(key, "1"); err != nil {
		return nil, err
	}
	return args, nil
}

// Prepare puts the bundled executable first on this process's PATH. It never
// kills, migrates or selects a tmux server: existing socket semantics still apply.
func Prepare(product string) error {
	if len(payload) == 0 {
		return nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir, err := unpack(filepath.Join(cache, product, "tmux"), payload)
	if err != nil {
		return fmt.Errorf("bundled tmux: %w", err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	// Popup shells inherit the outer tmux server's PATH; pass this absolute
	// executable explicitly rather than accidentally running its older tmux.
	if err := os.Setenv(strings.ToUpper(product)+"_BUNDLED_TMUX", filepath.Join(bin, "tmux")); err != nil {
		return err
	}
	if err := os.Setenv("STUI_TMUX_BIN", filepath.Join(bin, "tmux")); err != nil {
		return err
	}
	// Preserve custom terminfo first, and keep ncurses' default search (empty entry).
	return os.Setenv("TERMINFO_DIRS", os.Getenv("TERMINFO_DIRS")+":"+filepath.Join(dir, "terminfo")+":")
}

func unpack(cache string, data []byte) (string, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	dest := filepath.Join(cache, hash)
	ready := func() bool {
		b, err := os.ReadFile(filepath.Join(dest, ".complete"))
		fi, e := os.Stat(filepath.Join(dest, "bin", "tmux"))
		return err == nil && string(b) == hash && e == nil && fi.Mode().IsRegular() && fi.Mode()&0111 != 0
	}
	if ready() {
		return dest, nil
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(cache, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("invalid archive path %q", h.Name)
		}
		path := filepath.Join(stage, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0700); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if h.Size > 32<<20 {
				return "", fmt.Errorf("oversized archive member %q", h.Name)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return "", err
			}
			mode := os.FileMode(0600)
			if h.Mode&0111 != 0 {
				mode = 0700
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return "", err
			}
			_, err = io.Copy(f, tr)
			closeErr := f.Close()
			if err != nil {
				return "", err
			}
			if closeErr != nil {
				return "", closeErr
			}
		default:
			return "", fmt.Errorf("unsupported archive member %q", h.Name)
		}
	}
	fi, err := os.Stat(filepath.Join(stage, "bin", "tmux"))
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&0111 == 0 {
		return "", fmt.Errorf("archive has no executable tmux")
	}
	if err := os.WriteFile(filepath.Join(stage, ".complete"), []byte(hash), 0600); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dest); err != nil {
		// Another process can install the identical content concurrently. An
		// incomplete existing directory is an error, never silently removed.
		if !ready() {
			return "", err
		}
	}
	return dest, nil
}

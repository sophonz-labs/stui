package tmuxruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func archive(t *testing.T, name string, kind byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	h := &tar.Header{Name: name, Mode: 0755, Typeflag: kind}
	data := []byte("#!/bin/sh\nexit 0\n")
	if kind == tar.TypeReg {
		h.Size = int64(len(data))
	}
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestConcurrentUnpack(t *testing.T) {
	cache := t.TempDir()
	data := archive(t, "bin/tmux", tar.TypeReg)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			dir, err := unpack(cache, data)
			if err != nil {
				t.Error(err)
				return
			}
			got, err := os.ReadFile(filepath.Join(dir, "bin/tmux"))
			if err != nil || string(got) != "#!/bin/sh\nexit 0\n" {
				t.Errorf("executable: %q %v", got, err)
			}
		})
	}
	wg.Wait()
}

func TestRejectUnsafeArchive(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
	}{
		{"../outside", tar.TypeReg}, {"/outside", tar.TypeReg}, {"bin/tmux", tar.TypeSymlink}, {"wrong", tar.TypeReg},
	} {
		t.Run(tc.name+string(tc.kind), func(t *testing.T) {
			if _, err := unpack(t.TempDir(), archive(t, tc.name, tc.kind)); err == nil {
				t.Fatal("unsafe or incomplete archive accepted")
			}
		})
	}
}

func TestSelection(t *testing.T) {
	t.Setenv("DEMO_USE_EMBEDDED_TMUX", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := os.Getenv("PATH")
	args, err := Configure("demo", []string{"ui"})
	if err != nil || len(args) != 1 || args[0] != "ui" || os.Getenv("PATH") != path {
		t.Fatalf("default changed runtime: %v %v", args, err)
	}
	if len(payload) == 0 {
		if _, err := Configure("demo", []string{"--embedded-tmux"}); err == nil {
			t.Fatal("missing payload silently accepted")
		}
	}
}

func TestEmbeddedSelectionAndInheritance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := payload
	payload = archive(t, "bin/tmux", tar.TypeReg)
	t.Cleanup(func() { payload = old })
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, key := range []string{"DEMO_USE_EMBEDDED_TMUX", "DEMO_BUNDLED_TMUX", "STUI_TMUX_BIN", "TERMINFO_DIRS"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", os.Getenv("PATH"))
	args, err := Configure("demo", []string{"--embedded-tmux", "tmux", "-V"})
	if err != nil || len(args) != 2 || args[0] != "tmux" {
		t.Fatalf("selection: %v %v", args, err)
	}
	if _, err := os.Stat(os.Getenv("DEMO_BUNDLED_TMUX")); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DEMO_USE_EMBEDDED_TMUX") != "1" {
		t.Fatal("selection not inherited")
	}
	args, err = Configure("demo", []string{"ui"})
	if err != nil || len(args) != 1 || args[0] != "ui" {
		t.Fatalf("inherited selection: %v %v", args, err)
	}
}

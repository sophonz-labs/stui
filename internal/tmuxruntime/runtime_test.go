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

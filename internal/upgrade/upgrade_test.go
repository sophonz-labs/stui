package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// release serves latest = tag, with a tarball whose binary is body.
func release(t *testing.T, tag, body string, sum func(string) string) *httptest.Server {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("ok"))
	tw.WriteHeader(&tar.Header{Name: "stui", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	zw.Close()
	tgz := buf.Bytes()
	asset := fmt.Sprintf("stui-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	h := sha256.Sum256(tgz)
	good := hex.EncodeToString(h[:])
	if sum != nil {
		good = sum(good)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/version.txt", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, tag) })
	mux.HandleFunc("/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", good, asset)
	})
	mux.HandleFunc("/"+tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(tgz) })
	mux.HandleFunc("/api/releases/tags/"+tag, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"body":"the notes"}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func run(t *testing.T, s *httptest.Server, current, pin string, check bool) (string, string, string, error) {
	exe := filepath.Join(t.TempDir(), "stui")
	os.WriteFile(exe, []byte("old"), 0o755)
	var out bytes.Buffer
	tag, err := Run(Options{App: "stui", Current: current, Pin: pin, Check: check, Base: s.URL, API: s.URL + "/api", Exe: exe, Out: &out})
	b, _ := os.ReadFile(exe)
	return tag, string(b), out.String(), err
}

func TestUpgrade(t *testing.T) {
	s := release(t, "v0.2.0", "new", nil)
	tag, bin, out, err := run(t, s, "v0.1.9", "", false)
	if err != nil || tag != "v0.2.0" || bin != "new" {
		t.Fatalf("tag %q bin %q err %v\n%s", tag, bin, err, out)
	}
	if !strings.Contains(out, "the notes") {
		t.Errorf("no release notes in:\n%s", out)
	}
}

func TestCheckChangesNothing(t *testing.T) {
	s := release(t, "v0.2.0", "new", nil)
	tag, bin, out, err := run(t, s, "v0.1.9", "", true)
	if err != nil || tag != "" || bin != "old" || !strings.Contains(out, "v0.2.0 is available") {
		t.Fatalf("tag %q bin %q err %v\n%s", tag, bin, err, out)
	}
}

func TestChecksumMismatch(t *testing.T) {
	s := release(t, "v0.2.0", "new", func(string) string { return strings.Repeat("0", 64) })
	_, bin, _, err := run(t, s, "v0.1.9", "", false)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || bin != "old" {
		t.Fatalf("bin %q err %v", bin, err)
	}
}

func TestUpToDate(t *testing.T) {
	s := release(t, "v0.2.0", "new", nil)
	for _, cur := range []string{"v0.2.0", "v0.3.0"} {
		tag, bin, out, err := run(t, s, cur, "", false)
		if err != nil || tag != "" || bin != "old" {
			t.Fatalf("%s: tag %q bin %q err %v\n%s", cur, tag, bin, err, out)
		}
	}
}

func TestDowngradeOnlyWhenPinned(t *testing.T) {
	s := release(t, "v0.2.0", "new", nil)
	tag, bin, _, err := run(t, s, "v0.3.0", "v0.2.0", false)
	if err != nil || tag != "v0.2.0" || bin != "new" {
		t.Fatalf("tag %q bin %q err %v", tag, bin, err)
	}
}

func TestDevRefused(t *testing.T) {
	s := release(t, "v0.2.0", "new", nil)
	_, bin, _, err := run(t, s, "dev", "", false)
	if err == nil || !strings.Contains(err.Error(), "--version") || bin != "old" {
		t.Fatalf("bin %q err %v", bin, err)
	}
	if tag, bin, _, err := run(t, s, "dev", "v0.2.0", false); err != nil || tag != "v0.2.0" || bin != "new" {
		t.Fatalf("pinned dev: tag %q bin %q err %v", tag, bin, err)
	}
}

func TestUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	s := release(t, "v0.2.0", "new", nil)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	exe := filepath.Join(dir, "stui")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	_, err := Run(Options{App: "stui", Current: "v0.1.9", Base: s.URL, Exe: exe})
	if err == nil || !strings.Contains(err.Error(), "install.sh | sudo STUI_INSTALL_DIR="+dir+" sh") {
		t.Fatalf("err %v", err)
	}
}

func TestManaged(t *testing.T) {
	if managed("/opt/homebrew/Cellar/stui/0.2.0/bin/stui") != "Homebrew" || managed("/Users/a/.local/bin/stui") != "" {
		t.Fatal("managed")
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"v0.2.0", "v0.1.9", 1}, {"v0.10.0", "v0.9.0", 1}, {"v0.2.0", "v0.2.0-rc.3", 1}, {"v0.2.0-rc.1", "v0.2.0-rc.2", -1}, {"v1.0.0", "v1.0.0", 0}} {
		if got := compare(c.a, c.b); got != c.want {
			t.Errorf("compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
	if parse("dev") != nil || parse("v1.2") != nil || parse("v1.2.3-beta") != nil {
		t.Error("parse accepts a non-tag")
	}
}

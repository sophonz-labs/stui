// Package upgrade replaces the running binary with a release from
// dl.sophonz.io, the way install.sh puts it there: the tag in
// latest/version.txt, that tag's tarball, checked against its checksums.txt.
package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Options is one upgrade. The zero values of Base, API, Exe and Client are
// the real ones; tests point them elsewhere.
type Options struct {
	App     string // "stui"
	Current string // the running version, "dev" for a development build
	Pin     string // --version: this tag, even if older
	Check   bool   // --check: report, change nothing

	Base   string // https://dl.sophonz.io/<app>
	API    string // https://api.github.com/repos/sophonz-labs/<app>
	Exe    string // the binary to replace; default the running one
	Client *http.Client
	Out    io.Writer
}

// Main is `<app> upgrade [--check] [--version vX.Y.Z]`. It returns the tag
// it installed ("" when it installed nothing) and the exit code.
func Main(app, current string, args []string, out io.Writer) (string, int) {
	fs := flag.NewFlagSet(app+" upgrade", flag.ContinueOnError)
	fs.SetOutput(out)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	pin := fs.String("version", "", "install this release (vX.Y.Z), older or newer")
	fs.Usage = func() {
		fmt.Fprintf(out, "usage: %s upgrade [--check] [--version vX.Y.Z]\n\n", app)
		fmt.Fprintf(out, "Replaces this %s with the latest release from dl.sophonz.io, checked\nagainst its SHA-256. --version installs that release instead, older ones\ntoo; --check only says whether there is a newer one.\n", app)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return "", 0
		}
		return "", 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(out, "%s upgrade: unexpected argument %q\n", app, fs.Arg(0))
		return "", 2
	}
	tag, err := Run(Options{App: app, Current: current, Pin: *pin, Check: *check, Out: out})
	if err != nil {
		fmt.Fprintf(out, "%s upgrade: %v\n", app, err)
		return "", 1
	}
	return tag, 0
}

// Run does the upgrade and returns the tag it installed, "" for none.
func Run(o Options) (string, error) {
	if o.Base == "" {
		o.Base = "https://dl.sophonz.io/" + o.App
	}
	if o.API == "" {
		o.API = "https://api.github.com/repos/sophonz-labs/" + o.App
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	curl := fmt.Sprintf("curl -fsSL %s/install.sh | sh", o.Base)

	if o.Pin != "" && parse(o.Pin) == nil {
		return "", fmt.Errorf("--version %s is not a release tag like v0.2.0", o.Pin)
	}
	if o.Pin == "" && parse(o.Current) == nil {
		return "", fmt.Errorf("this is a %s build, not a release, so there is nothing to compare against; pass --version vX.Y.Z, or install a release: %s", o.Current, curl)
	}

	tag := o.Pin
	if tag == "" {
		b, err := get(o.Client, o.Base+"/latest/version.txt")
		if err != nil {
			return "", err
		}
		tag = strings.TrimSpace(string(b))
		if parse(tag) == nil {
			return "", fmt.Errorf("%s/latest/version.txt says %q, not a release tag", o.Base, tag)
		}
		switch c := compare(tag, o.Current); {
		case c == 0:
			fmt.Fprintf(o.Out, "%s %s is up to date\n", o.App, o.Current)
			return "", nil
		case c < 0:
			fmt.Fprintf(o.Out, "%s %s is newer than the latest release (%s); --version %s goes back to it\n", o.App, o.Current, tag, tag)
			return "", nil
		}
	} else if tag == o.Current {
		fmt.Fprintf(o.Out, "%s %s is already installed\n", o.App, tag)
		return "", nil
	}
	if o.Check {
		fmt.Fprintf(o.Out, "%s %s is available (this is %s); %s upgrade installs it\n", o.App, tag, o.Current, o.App)
		return "", nil
	}

	exe := o.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if pm := managed(exe); pm != "" {
		how := "upgrade it there"
		if pm == "Homebrew" {
			how = "brew upgrade " + o.App
		}
		return "", fmt.Errorf("%s was installed by %s, not by %s's install.sh: %s", exe, pm, o.App, how)
	}
	dir := filepath.Dir(exe)
	// Before downloading: the new binary is written next to the old one, so
	// the rename that swaps them never crosses a filesystem.
	tmp, err := os.CreateTemp(dir, "."+o.App+"-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to %s (%v); install there with:\n  curl -fsSL %s/install.sh | sudo %s_INSTALL_DIR=%s sh", dir, errors.Unwrap(err), o.Base, strings.ToUpper(o.App), dir)
	}
	defer os.Remove(tmp.Name()) // gone already after a successful rename
	defer tmp.Close()

	asset := fmt.Sprintf("%s-%s-%s.tar.gz", o.App, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(o.Out, "downloading %s %s (%s/%s)\n", o.App, tag, runtime.GOOS, runtime.GOARCH)
	sums, err := get(o.Client, o.Base+"/"+tag+"/checksums.txt")
	if err != nil {
		return "", err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("%s is not in %s's checksums.txt; nothing changed", asset, tag)
	}
	tgz, err := get(o.Client, o.Base+"/"+tag+"/"+asset)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(tgz); hex.EncodeToString(sum[:]) != strings.ToLower(want) {
		return "", fmt.Errorf("checksum mismatch for %s; nothing changed", asset)
	}
	bin, err := extract(tgz, o.App)
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(bin); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return "", err
	}
	fmt.Fprintf(o.Out, "%s %s -> %s  (%s)\n", o.App, o.Current, tag, exe)
	if notes := releaseNotes(o.Client, o.API, tag); notes != "" {
		fmt.Fprintf(o.Out, "\n%s\n", notes)
	}
	return tag, nil
}

// managed names the package manager that owns path, "" for none.
func managed(path string) string {
	switch {
	case strings.Contains(path, "/Cellar/"), strings.HasPrefix(path, "/home/linuxbrew/"):
		return "Homebrew"
	case strings.HasPrefix(path, "/nix/store/"):
		return "Nix"
	case strings.HasPrefix(path, "/snap/"):
		return "snap"
	case strings.HasPrefix(path, "/usr/bin/"), strings.HasPrefix(path, "/bin/"):
		return "the system's package manager"
	}
	return ""
}

func get(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// extract is the file called name at the top of the tarball.
func extract(tgz []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("the archive has no %s in it", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && strings.TrimPrefix(h.Name, "./") == name {
			return io.ReadAll(tr)
		}
	}
}

// releaseNotes is the GitHub release's text, "" when it cannot be had: they
// are a courtesy, and the upgrade is done by then.
func releaseNotes(c *http.Client, api, tag string) string {
	q := *c
	q.Timeout = 5 * time.Second
	b, err := get(&q, api+"/releases/tags/"+tag)
	if err != nil {
		return ""
	}
	var r struct{ Body string }
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	return strings.TrimSpace(r.Body)
}

// parse is vX.Y.Z (with an optional -rc.N) as numbers; nil when it is not one.
func parse(v string) []int {
	v, ok := strings.CutPrefix(v, "v")
	if !ok {
		return nil
	}
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 0, 4)
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out = append(out, n)
	}
	// A release sorts after every rc of it.
	rc := 1 << 30
	if hasPre {
		n, err := strconv.Atoi(strings.TrimPrefix(pre, "rc."))
		if err != nil || !strings.HasPrefix(pre, "rc.") {
			return nil
		}
		rc = n
	}
	return append(out, rc)
}

func compare(a, b string) int {
	x, y := parse(a), parse(b)
	for i := range x {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

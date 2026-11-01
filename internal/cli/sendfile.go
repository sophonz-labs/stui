package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/state"
)

// maxBrief is the largest brief stui worker send --file takes.
const maxBrief = 1 << 20

// sendFile: what stui worker send --file hands a worker. It is read once and
// kept as an unchanging copy in the run's sent/, the record, and the worker
// is typed one line saying where the copy is.
type sendFile struct {
	content string
	title   string
	name    string // the file's base name
	doc     string // always "": stui keeps no documents
	file    string // the copy
}

// readSendFile reads --file <path|-> once. The file itself is never written
// to. stui has no document store, so --doc is refused.
func readSendFile(s *state.Agent, file string, hasFile bool, docRef, title string, asJSON bool) *sendFile {
	receipt := map[string]any{}
	b := &sendFile{}
	switch {
	case docRef != "":
		die(asJSON, receipt, "stui keeps no documents: send the file with --file")
	case hasFile && file == "-":
		if term.IsTerminal(int(os.Stdin.Fd())) {
			die(asJSON, receipt, "--file - reads standard input: cat brief.md | stui worker send <id> --file -")
		}
		c, err := io.ReadAll(io.LimitReader(os.Stdin, maxBrief+1))
		if err != nil {
			die(asJSON, receipt, "read standard input: %v", err)
		}
		b.content = string(c)
	default:
		if file == "" {
			die(asJSON, receipt, "--file takes a file, or - for standard input")
		}
		fi, err := os.Stat(file)
		if err != nil {
			die(asJSON, receipt, "%v", err)
		}
		if !fi.Mode().IsRegular() {
			die(asJSON, receipt, "%s is not a file", file)
		}
		c, err := os.ReadFile(file)
		if err != nil {
			die(asJSON, receipt, "%v", err)
		}
		b.content, b.name = string(c), filepath.Base(file)
	}
	what := "the file"
	if file != "-" {
		what = file
	}
	switch c := []byte(b.content); {
	case len(bytes.TrimSpace(c)) == 0:
		die(asJSON, receipt, "not sent: %s is empty", what)
	case len(c) > maxBrief:
		die(asJSON, receipt, "not sent: %s is larger than 1 MB", what)
	case !utf8.Valid(c) || bytes.IndexByte(c, 0) >= 0:
		die(asJSON, receipt, "not sent: %s is not UTF-8 text", what)
	}
	b.title = cleanTitle(title)
	if b.title == "" {
		b.title = titleFor(b.content, b.name)
	}
	return b
}

// keep writes the copy under the run.
func (b *sendFile) keep(s *state.Agent, run, from, worker string, asJSON bool) {
	f, err := orch.WriteSent(run, worker, []byte(b.content), time.Now())
	if err != nil {
		die(asJSON, map[string]any{}, "not sent: keep the copy: %v", err)
	}
	b.file = f
}

// titleFor is the first markdown heading, else the file's name, else the
// first line.
func titleFor(content, name string) string {
	for _, line := range strings.Split(content, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "#") {
			if t := cleanTitle(strings.TrimLeft(l, "#")); t != "" {
				return t
			}
		}
	}
	if name != "" {
		if t := cleanTitle(strings.TrimSuffix(name, filepath.Ext(name))); t != "" {
			return t
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if t := cleanTitle(line); t != "" {
			return t
		}
	}
	return "brief"
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

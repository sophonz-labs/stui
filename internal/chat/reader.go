package chat

// Reading a file an agent is still appending to: from a byte offset,
// holding a line back until its newline is there, dropping a record too big
// to send, and telling a file that was replaced or cut short from one that
// grew. The rules (2 MB records, 64 KB steps back, the last 64 bytes as a
// fingerprint) are Orca's transcript-incremental-reader.ts,
// transcript-file-version.ts and transcript-tail-boundary.ts (MIT,
// Copyright (c) 2026 Lovecast Inc.).

import (
	"bytes"
	"errors"
	"io"
	"os"
)

const (
	// A record longer than this is dropped, not read.
	MaxRecord = 2 << 20
	// How far back each step of a backwards read goes.
	backStep = 64 << 10
	// The fingerprint of where a reader is: the bytes just before it.
	printLen = 64
	// How much a forward read takes at a time.
	readStep = 256 << 10
)

// Line is one complete record and where it starts.
type Line struct {
	Off  int64
	Data []byte
}

// fileVersion is what says a file is still the one a reader was reading.
type fileVersion struct {
	id    fileID
	off   int64
	print []byte
}

// Change says how a file changed under a reader.
type Change int

const (
	Same Change = iota
	// It grew: Read has more.
	Grew
	// Another file is at the path, it got shorter, or what was read is no
	// longer there: start over.
	Replaced
	// The path is gone.
	Gone
)

// Follower reads a file forward from an offset, a line at a time.
type Follower struct {
	path string
	// Everything before off has been returned (or dropped).
	off int64
	ver fileVersion
	// The size at the last look.
	size int64
	// Inside a record too long to keep: dropping until its newline.
	skipping bool
}

// NewFollower starts at off, which must be at the start of a line (0, or
// just after a newline).
func NewFollower(path string, off int64) (*Follower, error) {
	f := &Follower{path: path, off: off}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	f.size = st.Size()
	f.ver = fileVersion{id: idOf(st), off: off, print: fingerprint(fh, off)}
	return f, nil
}

// Offset is where the next line starts.
func (f *Follower) Offset() int64 { return f.off }

func fingerprint(fh *os.File, off int64) []byte {
	start := max(off-printLen, 0)
	b := make([]byte, off-start)
	n, _ := fh.ReadAt(b, start)
	return b[:n]
}

// Check says whether the file changed since the last read.
func (f *Follower) Check() Change {
	st, err := os.Stat(f.path)
	if err != nil {
		return Gone
	}
	if !sameID(idOf(st), f.ver.id) || st.Size() < f.off {
		return Replaced
	}
	if st.Size() != f.size {
		return Grew
	}
	return Same
}

// Verify reads the fingerprint again: the bytes before the offset are the
// ones read. Rewritten in place (same file, same size or longer) is
// Replaced. It costs a read of 64 bytes.
func (f *Follower) Verify() Change {
	fh, err := os.Open(f.path)
	if err != nil {
		return Gone
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return Gone
	}
	if !sameID(idOf(st), f.ver.id) || st.Size() < f.off {
		return Replaced
	}
	if !bytes.Equal(fingerprint(fh, f.off), f.ver.print) {
		return Replaced
	}
	if st.Size() != f.size {
		return Grew
	}
	return Same
}

var ErrReplaced = errors.New("file replaced")

// Read returns the complete lines added since the last read. A line still
// being written stays for the next read; one over MaxRecord is dropped.
// ErrReplaced: the file is not the one being read any more (Check).
func (f *Follower) Read() ([]Line, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !sameID(idOf(st), f.ver.id) || st.Size() < f.off || !bytes.Equal(fingerprint(fh, f.off), f.ver.print) {
		return nil, ErrReplaced
	}
	size := st.Size()
	f.size = size
	var out []Line
	pos := f.off
	var pending []byte // the line being put together, from lineStart
	lineStart := f.off
	buf := make([]byte, readStep)
	for pos < size {
		n, err := fh.ReadAt(buf[:min(int64(len(buf)), size-pos)], pos)
		if n == 0 {
			if err != nil && err != io.EOF {
				return out, err
			}
			break
		}
		chunk := buf[:n]
		for len(chunk) > 0 {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				if !f.skipping {
					pending = append(pending, chunk...)
					if len(pending) > MaxRecord {
						f.skipping, pending = true, nil
					}
				}
				pos += int64(len(chunk))
				chunk = nil
				break
			}
			if !f.skipping {
				pending = append(pending, chunk[:i]...)
				if len(pending) <= MaxRecord {
					if l := bytes.TrimRight(pending, "\r"); len(bytes.TrimSpace(l)) > 0 {
						out = append(out, Line{Off: lineStart, Data: bytes.Clone(l)})
					}
				}
			}
			f.skipping = false
			pending = pending[:0]
			pos += int64(i + 1)
			chunk = chunk[i+1:]
			lineStart = pos
			// Everything before here is done with.
			f.off = pos
		}
	}
	// A line without its newline yet is read again next time, unless it is
	// already too long: then the offset moves past what was dropped.
	if f.skipping {
		f.off = pos
	}
	f.ver = fileVersion{id: idOf(st), off: f.off, print: fingerprint(fh, f.off)}
	return out, nil
}

// lineEnd is the end of the last complete line at or before size: a
// snapshot reads up to there, and a follower starts there.
func lineEnd(fh *os.File, size int64) int64 {
	end := size
	buf := make([]byte, backStep)
	for end > 0 {
		start := max(end-backStep, 0)
		n, _ := fh.ReadAt(buf[:end-start], start)
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1
		}
		if size-start > MaxRecord {
			// No newline in the last 2 MB: a record being written that will
			// be dropped anyway. Start after it.
			return size
		}
		end = start
	}
	return 0
}

// readBack calls fn with each complete line that ends before end, last
// first, until fn says stop. end must be at the start of a line. Lines over
// MaxRecord are skipped.
func readBack(fh *os.File, end int64, fn func(Line) bool) error {
	buf := make([]byte, backStep)
	// The part of the line being put together that was already read (it
	// ends where the last line read starts).
	var carry []byte
	tooLong := false
	pos := end
	for pos > 0 {
		start := max(pos-backStep, 0)
		n, err := fh.ReadAt(buf[:pos-start], start)
		if err != nil && err != io.EOF {
			return err
		}
		chunk := buf[:n]
		for {
			i := bytes.LastIndexByte(chunk, '\n')
			if i < 0 {
				if !tooLong {
					carry = append(bytes.Clone(chunk), carry...)
					if len(carry) > MaxRecord {
						tooLong, carry = true, nil
					}
				}
				break
			}
			lineStart := start + int64(i) + 1
			if !tooLong && len(chunk)-i-1+len(carry) <= MaxRecord {
				line := append(bytes.Clone(chunk[i+1:]), carry...)
				if l := bytes.TrimRight(line, "\r"); len(bytes.TrimSpace(l)) > 0 {
					if !fn(Line{Off: lineStart, Data: l}) {
						return nil
					}
				}
			}
			tooLong, carry = false, nil
			chunk = chunk[:i]
		}
		pos = start
	}
	if !tooLong && len(bytes.TrimSpace(carry)) > 0 {
		fn(Line{Off: 0, Data: bytes.TrimRight(carry, "\r")})
	}
	return nil
}

// fileID is a file's identity (device and inode, or their Windows kin).
type fileID = os.FileInfo

func idOf(st os.FileInfo) fileID { return st }

func sameID(a, b fileID) bool { return a != nil && b != nil && os.SameFile(a, b) }

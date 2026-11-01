package chat

import (
	"errors"
	"os"
)

const (
	// Past the messages asked for, how much further back a snapshot reads
	// to find the prompt that started their turn.
	turnLookBack = 4 << 20
	// The most a snapshot holds while reading back.
	maxHeld = 16 << 20
	// The most messages one snapshot or page has.
	MaxTail = 200
)

// Window is the last messages before a point in a transcript.
type Window struct {
	Messages []Message
	// There are messages before these: ask again with Before.
	HasMore bool
	// Where the first of these starts (?before= for the page above).
	Before int64
	// The latest turn the window shows, nil when it shows none.
	Turn *Turn
	// Where the read ended: the end of the last complete line.
	End int64
}

var ErrBadOffset = errors.New("not the start of a record")

// ReadTail reads the last n messages of the transcript at path, written by
// agent. before > 0 reads the n before that offset instead (a message's
// Offset). The decoder it returns has read up to End, for a follower to go
// on with.
func ReadTail(path, agent string, before int64, n int) (Window, decoder, error) {
	return readTail(path, agent, -1, before, n)
}

// readTail is ReadTail up to end (-1: the end of the last complete line).
func readTail(path, agent string, end, before int64, n int) (Window, decoder, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Window{}, nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return Window{}, nil, err
	}
	if end < 0 || end > st.Size() {
		end = lineEnd(fh, st.Size())
	}
	w := Window{End: end}
	upto := end
	if before > 0 {
		if before > end {
			return w, nil, ErrBadOffset
		}
		var b [1]byte
		if _, err := fh.ReadAt(b[:], before-1); err != nil || b[0] != '\n' {
			return w, nil, ErrBadOffset
		}
		upto = before
	}
	n = max(1, min(n, MaxTail))
	probe := newDecoder(agent)
	if probe == nil {
		return w, nil, errors.New("unsupported agent")
	}

	var lines []Line // last first
	count, held := 0, 0
	var extra int64
	reachedStart := true
	readBack(fh, upto, func(l Line) bool {
		lines = append(lines, l)
		held += len(l.Data)
		if count >= n {
			extra += int64(len(l.Data))
		}
		msgs, _ := newDecoder(agent).decode(l.Data, l.Off)
		count += len(msgs)
		if count >= n && (probe.startsTurn(l.Data) || extra > turnLookBack) || held > maxHeld {
			reachedStart = l.Off == 0
			return false
		}
		return true
	})

	dec := newDecoder(agent)
	var all []Message
	at := map[string]int{}
	for i := len(lines) - 1; i >= 0; i-- {
		msgs, t := dec.decode(lines[i].Data, lines[i].Off)
		if t != nil {
			cp := *t
			w.Turn = &cp
		}
		for _, m := range msgs {
			// The same record again (a Codex item written twice): the later
			// one stands, where the first was.
			if j, ok := at[m.ID]; ok {
				all[j] = m
				continue
			}
			at[m.ID] = len(all)
			all = append(all, m)
		}
	}
	if len(all) > n {
		w.HasMore = true
		all = all[len(all)-n:]
	} else if len(lines) > 0 && !reachedStart {
		w.HasMore = true
	}
	w.Messages = all
	if len(all) > 0 {
		w.Before = all[0].Offset
	}
	if before > 0 {
		// A page above: the decoder is no use to anyone.
		return w, nil, nil
	}
	return w, dec, nil
}

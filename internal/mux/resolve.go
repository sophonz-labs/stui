package mux

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

type Choice string

const (
	Auto       Choice = "auto"
	TmuxChoice Choice = "tmux"
	ZellijMux  Choice = "zellij"
	PsmuxMux   Choice = "psmux"
	NoneChoice Choice = "none"
)

// Resolve picks a backend. auto prefers tmux, because it is the only one of
// the two that gives a phone its own size instead of resizing everyone else.
func Resolve(_ context.Context, choice Choice, command string) (Multiplexer, error) {
	if choice == NoneChoice {
		return NewNone(command), nil
	}
	tmux, zellij, psmux := NewTmux(), NewZellij(), NewPsmux()
	windows := runtime.GOOS == "windows"
	need := func(m Multiplexer) (Multiplexer, error) {
		if !m.Available() {
			return nil, fmt.Errorf("%s is not installed", m.ID())
		}
		return m, nil
	}
	switch choice {
	case TmuxChoice:
		if windows {
			// Any tmux there is Cygwin's or MSYS2's, which cannot drive a
			// Windows console program. psmux is tmux for Windows.
			if !psmux.Available() {
				return nil, errors.New("tmux does not run on Windows, and psmux, which stands in for it, is not here either -- try --mux zellij")
			}
			return psmux, nil
		}
		return need(tmux)
	case PsmuxMux:
		if !windows {
			return nil, errors.New("psmux is tmux for Windows -- use --mux tmux here")
		}
		return need(psmux)
	case ZellijMux:
		return need(zellij)
	case Auto, "":
	default:
		return nil, fmt.Errorf("unknown multiplexer: %s", choice)
	}
	order := []Multiplexer{tmux, zellij}
	if windows {
		order = []Multiplexer{psmux, zellij}
	}
	for _, m := range order {
		if m.Available() {
			return m, nil
		}
	}
	return NewNone(command), nil
}

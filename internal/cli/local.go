package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// What is left of stui's workers on other machines: stui runs on one, so a
// worker id is a pane id (%N) or a worker's name, never <machine>/%N.

var errOneMachine = errors.New("stui runs on one machine: workers are panes here")

type machine struct{ Slug string }

func splitRemote(id string) (slug, pane string, ok bool) { return "", "", false }

func isWorkerID(id string) bool { return strings.HasPrefix(id, "%") }

func resolveMachine(arg string) (machine, error) { return machine{}, errOneMachine }

func tryRemoteClient(slug string) (struct{}, error) { return struct{}{}, errOneMachine }

func remoteWorker(id string, asJSON bool) (*orchPane, agentPane, error) {
	return nil, agentPane{}, errOneMachine
}

func workerStartRemote(pane string, sp startSpec, m string, receipt map[string]any, asJSON bool) string {
	die(asJSON, receipt, "%v (--machine is not for stui)", errOneMachine)
	return ""
}

func remoteRead(id string, showBrief, asJSON bool) {
	die(asJSON, map[string]any{"id": id}, "%v", errOneMachine)
}

func remoteSend(me, id, text string, b *sendFile, asJSON bool) {
	die(asJSON, map[string]any{"id": id}, "%v", errOneMachine)
}

func remoteStop(id string, asJSON bool) { die(asJSON, map[string]any{"id": id}, "%v", errOneMachine) }

func remoteRows(run string, asJSON bool) []map[string]any { return nil }

func parentHere(parent, run string) (gone bool, now string, known bool) { return false, "", false }

func machineLabel(slug string) string { return slug }

func ago(at int64) string {
	s := max(0, (time.Now().UnixMilli()-at+500)/1000)
	round := func(n, d int64) int64 { return (n + d/2) / d }
	switch {
	case s < 60:
		return fmt.Sprintf("%ds ago", s)
	case s < 3600:
		return fmt.Sprintf("%dm ago", round(s, 60))
	case s < 86400:
		return fmt.Sprintf("%dh ago", round(s, 3600))
	}
	return fmt.Sprintf("%dd ago", round(s, 86400))
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

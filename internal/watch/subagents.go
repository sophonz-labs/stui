package watch

import (
	"sort"
	"strings"
	"time"
)

// Each subagent a pane's agent starts, one by one: its type, the short
// description the main agent gave the Agent tool, and whether it is running,
// done or lost (section 1, subagents). The prompt it was given is never
// kept (stui hook drops it).
//
// SubagentStart carries the subagent's id and type but not the tool call
// that started it, so the description comes from the main agent's
// PreToolUse for the Agent tool: the oldest one not yet taken whose type is
// the subagent's. Its PostToolUse names the run (tool_response.agentId), which
// settles it when the order was not the obvious one.

// Keep the item list small: every running subagent, then the most recent
// ended ones up to this many in all.
const maxSubItems = 12

// spawnWait is how long an Agent call waits for its SubagentStart.
const spawnWait = 2 * time.Minute

type spawn struct {
	toolUseID, typ, desc, ref string
	at                        time.Time
}

// noteSpawnLocked takes the main agent's Agent tool call: PreToolUse to wait
// for its SubagentStart, PostToolUse to name the run it started.
func (p *paneState) noteSpawnLocked(event string, data map[string]any, now time.Time) {
	str := func(k string) string { s, _ := data[k].(string); return s }
	desc, typ, tid, ref := str("agent_desc"), str("agent_subtype"), str("tool_use_id"), str("agent_ref")
	if desc == "" {
		desc = str("agent_name")
	}
	// Calls that never got their SubagentStart (refused, failed) go.
	kept := p.spawns[:0]
	for _, s := range p.spawns {
		if now.Sub(s.at) < spawnWait {
			kept = append(kept, s)
		}
	}
	p.spawns = kept
	switch event {
	case "PreToolUse":
		if len(p.spawns) < 16 {
			p.spawns = append(p.spawns, spawn{toolUseID: tid, typ: typ, desc: desc, at: now})
		}
	case "PostToolUse", "PostToolUseFailure":
		if ref != "" {
			if it := p.subItems[ref]; it != nil {
				if desc != "" {
					it.Description = desc
				}
				if typ != "" && it.Type == "" {
					it.Type = typ
				}
				p.dropSpawn(tid)
				return
			}
		}
		for i := range p.spawns {
			if p.spawns[i].toolUseID == tid && tid != "" {
				if ref == "" {
					// Failed, or a foreground run that is over.
					p.dropSpawn(tid)
				} else {
					p.spawns[i].ref = ref
				}
				return
			}
		}
	}
}

func (p *paneState) dropSpawn(tid string) {
	if tid == "" {
		return
	}
	for i, s := range p.spawns {
		if s.toolUseID == tid {
			p.spawns = append(p.spawns[:i], p.spawns[i+1:]...)
			return
		}
	}
}

// takeSpawn is the waiting Agent call subagent id of type typ came from.
func (p *paneState) takeSpawn(id, typ string) string {
	pick := -1
	for i, s := range p.spawns {
		if s.ref != "" && s.ref == id {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i, s := range p.spawns {
			if s.ref == "" && (s.typ == typ || s.typ == "" && typ == "general-purpose") {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		return ""
	}
	desc := p.spawns[pick].desc
	p.spawns = append(p.spawns[:pick], p.spawns[pick+1:]...)
	return desc
}

// subStartLocked is SubagentStart: a new item, or one seen before running
// again (a resumed subagent, an in-process teammate taking a new message).
func (p *paneState) subStartLocked(id, typ string, now time.Time) {
	if p.subItems == nil {
		p.subItems = map[string]*SubagentItem{}
	}
	ms := now.UnixMilli()
	if it := p.subItems[id]; it != nil {
		it.State, it.StartedAt, it.EndedAt = SubRunning, ms, nil
		if typ != "" {
			it.Type = typ
		}
		return
	}
	desc := p.takeSpawn(id, typ)
	if desc == "" {
		// An in-process teammate is <name>@<team>.
		if name, _, ok := strings.Cut(id, "@"); ok {
			desc = name
		}
	}
	p.subItems[id] = &SubagentItem{ID: id, Type: typ, Description: desc, State: SubRunning, StartedAt: ms}
	p.pruneSubItems()
}

// subEndLocked marks item id done or lost.
func (p *paneState) subEndLocked(id, state string, now time.Time) {
	it := p.subItems[id]
	if it == nil {
		return
	}
	ms := now.UnixMilli()
	it.State, it.EndedAt = state, &ms
	p.pruneSubItems()
}

// subReviveLocked is a lost subagent heard from again.
func (p *paneState) subReviveLocked(id string) {
	if it := p.subItems[id]; it != nil {
		it.State, it.EndedAt = SubRunning, nil
	}
}

// clearEndedSubs drops the ended items: a new turn starts with only what is
// still running, as the done count does.
func (p *paneState) clearEndedSubs() {
	for id, it := range p.subItems {
		if it.State != SubRunning {
			delete(p.subItems, id)
		}
	}
}

// pruneSubItems drops the oldest ended items beyond maxSubItems.
func (p *paneState) pruneSubItems() {
	if len(p.subItems) <= maxSubItems {
		return
	}
	var ended []*SubagentItem
	for _, it := range p.subItems {
		if it.State != SubRunning {
			ended = append(ended, it)
		}
	}
	sort.Slice(ended, func(i, j int) bool { return endOf(ended[i]) < endOf(ended[j]) })
	for _, it := range ended {
		if len(p.subItems) <= maxSubItems {
			break
		}
		delete(p.subItems, it.ID)
	}
}

func endOf(it *SubagentItem) int64 {
	if it.EndedAt != nil {
		return *it.EndedAt
	}
	return it.StartedAt
}

// subItemsView is the items as the API shows them: running first, then
// ended, each oldest first; at most maxSubItems unless more are running.
func (p *paneState) subItemsView() []SubagentItem {
	out := make([]SubagentItem, 0, len(p.subItems))
	for _, it := range p.subItems {
		v := *it
		if it.EndedAt != nil {
			e := *it.EndedAt
			v.EndedAt = &e
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].State == SubRunning, out[j].State == SubRunning
		if ri != rj {
			return ri
		}
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

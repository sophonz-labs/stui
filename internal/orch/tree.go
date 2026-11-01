package orch

// The tree's own history: who made whom a worker of whom, let it go,
// renamed it or gave it a role, and when.
//
// The events of a run said what was handed over and who reported, and the
// parent at that moment came with a report by chance. A worker put under
// the wrong parent after a reboot could be found only from its reports; one that had not reported since could not
// be found at all, and who did it was nowhere. Now each change is an event,
// with the parents by their lasting keys.

import (
	"context"
	"fmt"
)

type byCtx struct{}

// WithBy is ctx for a request asked by pane by ("%9"), or by an app ("").
func WithBy(ctx context.Context, by string) context.Context {
	return context.WithValue(ctx, byCtx{}, by)
}

// treeEvent writes ev with who asked: the pane the request said, or "app".
func (a *Adopter) treeEvent(ctx context.Context, ps map[string]pane, ev Event) {
	if ev.Run == "" {
		return
	}
	by, _ := ctx.Value(byCtx{}).(string)
	switch {
	case paneID.MatchString(by):
		ev.From = by
		ev.FromName = callOf(ps[by])
	case by != "":
		// Another machine of the account, by its slug: its stui asked.
		ev.By = by
	default:
		ev.By = "app"
	}
	AppendTreeEvent(ev)
}

// AppendTreeEvent writes a change of the tree, with its title, for an stui
// that made the change itself (the CLI's stui worker about).
func AppendTreeEvent(ev Event) {
	if ev.Run == "" {
		return
	}
	if ev.Title == "" {
		ev.Title = treeTitle(ev)
	}
	AppendEvent(ev)
}

// callOf is what a pane is called in a run: its name, or "coordinator"
// for the top of one (or a pane about to be).
func callOf(p pane) string {
	if p.Name == "" && p.ID != "" && (p.Role == RoleCoordinatorName || (p.Role == "" && p.Parent == "")) {
		return "coordinator"
	}
	return p.Name
}

// treeTitle is the one line stui orch history and the apps show.
func treeTitle(ev Event) string {
	side := func(id, name string) string {
		if name != "" {
			return name
		}
		return id
	}
	switch ev.Kind {
	case EvAdopt:
		t := "worker of " + side(ev.Parent, ev.ParentName)
		if ev.WasParent != "" && ev.WasParent != ev.Parent {
			t += " (was " + ev.WasParent + ")"
		}
		if ev.Was != "" && ev.Was != ev.Now {
			t += fmt.Sprintf("; named %s, was %s", ev.Now, ev.Was)
		}
		return t
	case EvRelease:
		return "no longer a worker of " + ev.WasParent
	case EvRename:
		return ev.Was + " → " + ev.Now
	case EvAbout:
		if ev.Now == "" {
			return "role cleared"
		}
		return "role: " + ev.Now
	}
	return ""
}

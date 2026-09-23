package app

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Pinned tasks live in their own panel, can come from any list and survive restarts: they
// are stored in the cache per workspace, shown from there at once and refreshed after.

func (a *App) pinnedKey() string { return "ui:pinned:" + a.TeamID }

func (a *App) findPinned(id string) int {
	return slices.IndexFunc(a.Pinned, func(t *clickup.Task) bool { return t.ID == id })
}

// IsPinned reports whether the task with this id is pinned.
func (a *App) IsPinned(id string) bool { return a.findPinned(id) >= 0 }

func (a *App) loadPinned() {
	a.Pinned = pointers(cache.Value(a.Cache, a.pinnedKey(), []clickup.Task{}))
	a.PinnedSel = min(a.PinnedSel, max(len(a.Pinned)-1, 0))
	a.RefreshPinned()
}

func (a *App) persistPinned() {
	values := make([]clickup.Task, 0, len(a.Pinned))
	for _, t := range a.Pinned {
		values = append(values, *t)
	}
	_ = cache.Put(a.Cache, a.pinnedKey(), values)
}

// TogglePin pins the current task, or unpins it when it already is.
func (a *App) TogglePin() {
	t := a.current()
	if t == nil {
		return
	}
	if i := a.findPinned(t.ID); i >= 0 {
		shownPin := a.Detail == a.Pinned[i]
		a.removePinned(i)
		if shownPin { // unpinned from the pinned panel: show what's highlighted there now
			a.Detail, a.Comments = nil, nil
			a.SelectPinned(a.PinnedSel)
		}
		a.log("Unpinned " + t.Label())
		a.UI.Notify(Info, "Unpinned "+t.Label())
	} else {
		pinned := *t // a copy of its own: list reloads don't replace it
		a.Pinned = append(a.Pinned, &pinned)
		a.log("Pinned " + t.Label())
		a.UI.Notify(Info, "Pinned "+t.Label()+" (4 to see pinned tasks)")
	}
	a.persistPinned()
}

// removePinned drops pin i, keeping the highlight on the same task where possible.
func (a *App) removePinned(i int) {
	a.Pinned = slices.Delete(slices.Clone(a.Pinned), i, i+1)
	if i < a.PinnedSel {
		a.PinnedSel--
	}
	a.PinnedSel = min(a.PinnedSel, max(len(a.Pinned)-1, 0))
	a.persistPinned()
}

// SelectPinned highlights pinned task i and shows it in the detail panel.
func (a *App) SelectPinned(i int) {
	if len(a.Pinned) == 0 {
		return
	}
	a.PinnedSel = min(max(i, 0), len(a.Pinned)-1)
	if t := a.Pinned[a.PinnedSel]; a.Detail != t {
		a.LoadDetail(t)
	}
}

// RefreshPinned fetches all pinned tasks in parallel. Tasks deleted in ClickUp are unpinned.
func (a *App) RefreshPinned() {
	if len(a.Pinned) == 0 {
		return
	}
	ids := make([]string, len(a.Pinned))
	for i, t := range a.Pinned {
		ids[i] = t.ID
	}
	touched := maps.Clone(a.touch)
	a.run("pinned", func(ctx context.Context, apply func(func())) {
		fresh := make([]clickup.Task, len(ids))
		errs := make([]error, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Go(func() { fresh[i], errs[i] = a.API.GetTask(ctx, id, "") })
		}
		wg.Wait()
		apply(func() {
			for i, id := range ids {
				j := a.findPinned(id)
				if j < 0 {
					continue // unpinned meanwhile
				}
				if e, ok := errors.AsType[*clickup.APIError](errs[i]); ok && e.Status == http.StatusNotFound {
					a.log("Unpinned " + a.Pinned[j].Label() + ": it no longer exists in ClickUp")
					if a.Detail == a.Pinned[j] {
						a.Detail, a.Comments = nil, nil
					}
					a.removePinned(j)
					continue
				}
				switch {
				case errs[i] != nil:
					a.log(style.Dim("Refreshing pinned " + a.Pinned[j].Label() + ": " + errs[i].Error()))
				case a.touch[id] == touched[id]: // skip data older than a local edit
					*a.Pinned[j] = fresh[i]
					a.propagate(a.Pinned[j])
				}
			}
			a.persistPinned()
		})
	})
}

// propagate copies t into every other copy of the same task (list and pinned), so an edit
// made through one shows everywhere.
func (a *App) propagate(t *clickup.Task) {
	for _, list := range [][]*clickup.Task{a.Tasks, a.Pinned} {
		for _, other := range list {
			if other.ID == t.ID && other != t {
				*other = *t
			}
		}
	}
	if a.IsPinned(t.ID) {
		a.persistPinned()
	}
}

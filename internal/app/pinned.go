package app

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

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
func (a *App) RefreshPinned() { a.refreshPinned(false) }

// pinnedEvery is how often the automatic refresh fetches pinned tasks that aren't in the task
// list; those that are get updated with the list, for free (syncPinned).
const pinnedEvery = 5 * time.Minute

// refreshPinned fetches the pinned tasks, or with outsideList only those the task list doesn't
// hold, one request each.
func (a *App) refreshPinned(outsideList bool) {
	a.pinnedAt = a.Now()
	var ids []string
	for _, t := range a.Pinned {
		if !outsideList || a.find(t.ID) == nil {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return
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
					replace(a.Pinned[j], fresh[i])
					a.propagate(a.Pinned[j])
				}
			}
			a.persistPinned()
		})
	})
}

// syncPinned updates pinned tasks that are also in the freshly loaded task list from it, so they
// need no request of their own.
func (a *App) syncPinned() {
	changed := false
	for _, p := range a.Pinned {
		if row := a.find(p.ID); row != nil && !row.Pending && row.DateUpdated.Int() > p.DateUpdated.Int() {
			replace(p, *row)
			changed = true
		}
	}
	if changed {
		a.persistPinned()
	}
}

// replace overwrites a copy of a task with newer data, except for its position. ClickUp's
// orderindex depends on where the task came from: fetched on its own, a task reports another
// one than in its list. Only a list load may move a row, or rows jump when you select them.
func replace(dst *clickup.Task, src clickup.Task) {
	old := *dst
	*dst = src
	dst.OrderIndex = old.OrderIndex
	keepDetails(dst, &old)
}

// keepDetails fills in what only a task fetched on its own carries, the tracked time and the
// subtasks, from an older copy when the newer data came from a list, which leaves them out.
func keepDetails(dst, older *clickup.Task) {
	if dst.TimeSpent == "" {
		dst.TimeSpent = older.TimeSpent
	}
	if dst.Subtasks == nil {
		dst.Subtasks = older.Subtasks
	}
}

// propagate copies t into every other copy of the same task (list and pinned), so an edit
// made through one shows everywhere.
func (a *App) propagate(t *clickup.Task) {
	for _, list := range [][]*clickup.Task{a.Tasks, a.Pinned} {
		for _, other := range list {
			if other.ID == t.ID && other != t {
				replace(other, *t)
			}
		}
	}
	if a.IsPinned(t.ID) {
		a.persistPinned()
	}
}

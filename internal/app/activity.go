package app

import (
	"cmp"
	"context"
	"errors"
	"sync"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// activity is what the API tells about a task's history, besides its comments.
type activity struct {
	Entries []clickup.TimeEntry   `json:"entries"`
	History *clickup.TimeInStatus `json:"history,omitzero"` // nil when the plan lacks it
}

// Events is the activity of the task in the task panel, merged with its comments by the task
// panel; nil until it's loaded.
func (a *App) Events() []render.Event {
	if a.Detail == nil || a.activityID != a.Detail.ID {
		return nil
	}
	events := render.Activity(a.Detail, a.activity.Entries, a.activity.History)
	if events == nil {
		events = []render.Event{} // loaded, but nothing happened yet
	}
	return events
}

// LoadActivity loads the activity of the task in the task panel. It costs requests, so the UI
// asks for it when the panel is opened, not for every task scrolled past; it's reused for two
// minutes.
func (a *App) LoadActivity() { a.loadActivity(false) }

func (a *App) loadActivity(force bool) {
	t := a.Detail
	if t == nil || t.Pending {
		return
	}
	id, teamID, key := t.ID, a.TeamID, "activity:"+t.ID
	cached, at, ok := cache.Get[activity](a.Cache, key)
	if ok {
		a.activity, a.activityID = cached, id
		if !force && time.Since(at) < detailFresh { // the cache's clock is the wall clock
			return
		}
	}
	// What this workspace turned out not to allow, so it isn't asked again.
	noHistory := cache.Value(a.Cache, "no-status-history:"+teamID, false)
	onlyMine := cache.Value(a.Cache, "only-my-time:"+teamID, false)
	var everyone []int64
	if !onlyMine {
		for _, u := range a.Members() {
			everyone = append(everyone, u.ID)
		}
	}
	from := a.Now().AddDate(-1, 0, 0)
	if created, ok := render.Millis(t.DateCreated); ok {
		from = created.AddDate(0, 0, -1)
	}
	to := a.Now().AddDate(0, 0, 1)
	a.run("activity", func(ctx context.Context, apply func(func())) {
		var act activity
		var errEntries, errHistory error
		var lostHistory, lostOthers bool
		var wg sync.WaitGroup
		wg.Go(func() {
			act.Entries, errEntries = a.API.TimeEntries(ctx, teamID, from, to, id, everyone...)
			var apiErr *clickup.APIError
			if len(everyone) > 0 && errors.As(errEntries, &apiErr) && apiErr.Status < 500 {
				// Only admins may see others' time: show mine.
				lostOthers = true
				act.Entries, errEntries = a.API.TimeEntries(ctx, teamID, from, to, id)
			}
		})
		if !noHistory {
			wg.Go(func() {
				h, err := a.API.TimeInStatus(ctx, id)
				switch {
				case err == nil:
					act.History = &h
				case clickup.NotOnPlan(err):
					lostHistory = true
				default:
					errHistory = err
				}
			})
		}
		wg.Wait()
		apply(func() {
			if lostHistory {
				_ = cache.Put(a.Cache, "no-status-history:"+teamID, true)
				a.log(style.Dim("Status history (Time in Status) isn't on this workspace's plan: activity shows time logged only"))
			}
			if lostOthers {
				_ = cache.Put(a.Cache, "only-my-time:"+teamID, true)
				a.log(style.Dim("Only workspace admins see everyone's time: activity shows your time entries"))
			}
			if err := cmp.Or(errEntries, errHistory); err != nil {
				a.error("Loading activity", err)
				return
			}
			_ = cache.Put(a.Cache, key, act)
			if a.Detail != nil && a.Detail.ID == id {
				a.activity, a.activityID = act, id
			}
		})
	})
}

package render

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Event is something that happened to a task, shown between its comments like ClickUp's
// activity. ClickUp's API has no activity log, so it's pieced together from what it does
// tell: when the task was created, done and closed, the time logged on it, and (on paid
// plans) when it entered each status.
type Event struct {
	At   time.Time
	Who  string // empty when the API doesn't say
	What string // may contain styling
}

// Activity lists a task's events, oldest first. history is nil when it isn't available.
func Activity(t *clickup.Task, entries []clickup.TimeEntry, history *clickup.TimeInStatus) []Event {
	var out []Event
	add := func(ms clickup.FlexString, who, what string) {
		if at, ok := Millis(ms); ok {
			out = append(out, Event{At: at, Who: who, What: what})
		}
	}
	creator := ""
	if t.Creator != nil {
		creator = t.Creator.Username
	}
	add(t.DateCreated, creator, "created this task")
	if history != nil {
		seen := map[string]bool{}
		for i, s := range append(slices.Clone(history.History), history.Current) {
			key := s.Status + "@" + string(s.TotalTime.Since)
			if seen[key] || s.Status == "" || (i == 0 && s.TotalTime.Since == t.DateCreated) {
				continue // the status it was created in isn't a change
			}
			seen[key] = true
			add(s.TotalTime.Since, "", "moved to "+style.Fg(s.Color, style.Bold)(strings.ToUpper(s.Status)))
		}
	}
	for _, e := range entries {
		what := "logged " + style.Bold(parse.Hours(time.Duration(e.Duration.Int())*time.Millisecond))
		if e.Running() {
			what = "started a timer"
		} else if e.Description != "" {
			what += " — " + e.Description
		}
		add(cmp.Or(e.At, e.Start), e.User.Username, what)
	}
	if t.DateDone != "" && t.DateDone != t.DateClosed {
		add(t.DateDone, "", "marked it done")
	}
	add(t.DateClosed, "", "closed it")
	slices.SortStableFunc(out, func(a, b Event) int { return a.At.Compare(b.At) })
	return out
}

// eventLine renders an event as one dim line among the comments.
func eventLine(e Event) string {
	who := cmp.Or(e.Who, "ClickUp")
	return style.Dim("• ") + style.Cyan(who) + " " + e.What + style.Dim("  "+e.At.Format("2006-01-02 15:04"))
}

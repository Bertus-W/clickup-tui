package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Timesheet is the weekly time page: my entries for one week, shown per task and day.
type Timesheet struct {
	Week     time.Time // Monday 00:00
	Entries  []clickup.TimeEntry
	Extra    []render.SheetTask // tasks added to the sheet that have no time yet
	Loading  bool
	Row, Col int
}

const logHint = "1h30 · 45m fixed login · 2h yesterday · 1:30 mon 09:00 review"

func (a *App) weekKey(week time.Time) string {
	return "time:" + a.TeamID + ":" + week.Format(time.DateOnly)
}

// TimerRunning reports whether a timer runs; safe from any goroutine (the UI ticks the clock).
func (a *App) TimerRunning() bool { return a.timerRunning.Load() }

func (a *App) setTimer(e *clickup.TimeEntry) {
	a.Timer = e
	a.timerRunning.Store(e != nil)
}

// --- the timesheet page -------------------------------------------------------------------

// OpenTimesheet shows the current week (or the week you left it on).
func (a *App) OpenTimesheet() {
	if a.Sheet.Week.IsZero() {
		a.Sheet.Week = render.WeekStart(a.Now())
		a.Sheet.Col = max(render.DayOfWeek(a.Now(), a.Sheet.Week), 0)
	}
	a.LoadWeek()
}

// ShiftWeek moves the sheet by delta weeks; 0 jumps back to this week.
func (a *App) ShiftWeek(delta int) {
	if delta == 0 {
		a.Sheet.Week = render.WeekStart(a.Now())
		a.Sheet.Col = max(render.DayOfWeek(a.Now(), a.Sheet.Week), 0)
	} else {
		a.Sheet.Week = a.Sheet.Week.AddDate(0, 0, 7*delta)
	}
	a.Sheet.Extra, a.Sheet.Row = nil, 0
	a.LoadWeek()
}

// LoadWeek shows the cached week at once, then refreshes it.
func (a *App) LoadWeek() {
	week, teamID := a.Sheet.Week, a.TeamID
	cached, _, hit := cache.Get[[]clickup.TimeEntry](a.Cache, a.weekKey(week))
	a.Sheet.Entries, a.Sheet.Loading = cached, !hit
	a.run("week", func(ctx context.Context, apply func(func())) {
		entries, err := a.API.TimeEntries(ctx, teamID, week, week.AddDate(0, 0, 7), "")
		if err == nil {
			_ = cache.Put(a.Cache, a.weekKey(week), entries)
		}
		apply(func() {
			if !a.Sheet.Week.Equal(week) {
				return
			}
			a.Sheet.Loading = false
			if err != nil {
				a.error("Loading time entries", err)
				return
			}
			a.Sheet.Entries = entries
			a.clampSheet()
		})
	})
}

// SheetRows is the grid for the current week.
func (a *App) SheetRows() ([]render.SheetRow, [7]time.Duration, time.Duration) {
	return render.Sheet(a.Sheet.Entries, a.Sheet.Week, a.Sheet.Extra, a.Now())
}

func (a *App) clampSheet() {
	rows, _, _ := a.SheetRows()
	a.Sheet.Row = min(max(a.Sheet.Row, 0), max(len(rows)-1, 0))
	a.Sheet.Col = min(max(a.Sheet.Col, 0), 6)
}

// SheetMove moves the selected cell.
func (a *App) SheetMove(rows, cols int) {
	a.Sheet.Row += rows
	a.Sheet.Col += cols
	a.clampSheet()
}

func (a *App) sheetCell() (render.SheetRow, time.Time, bool) {
	rows, _, _ := a.SheetRows()
	if a.Sheet.Row >= len(rows) {
		return render.SheetRow{}, time.Time{}, false
	}
	return rows[a.Sheet.Row], a.Sheet.Week.AddDate(0, 0, a.Sheet.Col), true
}

// SelectedEntries are the entries behind the selected cell.
func (a *App) SelectedEntries() []clickup.TimeEntry {
	row, _, ok := a.sheetCell()
	if !ok {
		return nil
	}
	return row.Entries[a.Sheet.Col]
}

// EditCell edits the selected cell: type the hours when it has one entry or none, or pick
// which entry to edit when it has several.
func (a *App) EditCell() {
	row, day, ok := a.sheetCell()
	if !ok {
		a.UI.Notify(Warn, "No tasks this week yet. Press a to add one, or L on a task to log time.")
		return
	}
	title := day.Format("Mon 02 Jan") + " · " + row.Name
	task := &clickup.EntryTask{ID: clickup.FlexString(row.TaskID), CustomID: row.Label, Name: row.Name}
	switch entries := row.Entries[a.Sheet.Col]; len(entries) {
	case 0:
		a.logPrompt(title, task, day)
	case 1:
		a.editEntry(entries[0], title)
	default:
		items := make([]MenuItem, 0, len(entries)+1)
		for i, e := range entries[:min(len(entries), len(optionKeys))] {
			items = append(items, MenuItem{Key: optionKeys[i], Label: render.EntryLine(e, a.Now(), false), Value: e})
		}
		items = append(items, MenuItem{Key: 'a', Label: style.Green("+ add an entry"), Value: nil})
		a.UI.Menu(title, items, 0, func(item MenuItem) {
			if e, ok := item.Value.(clickup.TimeEntry); ok {
				a.editEntry(e, title)
			} else {
				a.logPrompt(title, task, day)
			}
		})
	}
}

// ClearCell deletes every entry in the selected cell.
func (a *App) ClearCell() {
	row, day, ok := a.sheetCell()
	entries := a.SelectedEntries()
	if !ok || len(entries) == 0 {
		return
	}
	var total time.Duration
	for _, e := range entries {
		total += e.Length(a.Now())
	}
	msg := fmt.Sprintf("Delete %s (%s) on %s for %s?", parse.Hours(total), plural(len(entries), "entry", "entries"),
		day.Format("Mon 02 Jan"), row.Name)
	a.UI.Confirm("Delete time", msg, func() {
		for _, e := range entries {
			a.deleteEntry(e)
		}
	})
}

// AddSheetTask adds a task row to the sheet, picked from pinned and visible tasks.
func (a *App) AddSheetTask() {
	seen := map[string]bool{}
	var options []Option
	var tasks []*clickup.Task
	for _, t := range slices.Concat(a.Pinned, []*clickup.Task{a.Detail}, a.Tasks) {
		if t == nil || t.Pending || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		tasks = append(tasks, t)
		options = append(options, Option{ID: t.ID, Label: style.Dim(t.Label()) + " " + t.Name + style.Dim("  "+t.List.Name)})
	}
	if len(options) == 0 {
		a.UI.Notify(Warn, "Open a list or pin tasks first: the sheet offers those.")
		return
	}
	a.UI.Pick("Add a task to the timesheet", options, "", func(id string) {
		t := tasks[slices.IndexFunc(tasks, func(t *clickup.Task) bool { return t.ID == id })]
		rows, _, _ := a.SheetRows()
		if !slices.ContainsFunc(rows, func(r render.SheetRow) bool { return r.TaskID == id }) {
			a.Sheet.Extra = append(a.Sheet.Extra, render.SheetTask{ID: t.ID, Label: t.Label(), Name: t.Name, List: t.List.Name})
		}
		rows, _, _ = a.SheetRows()
		a.Sheet.Row = slices.IndexFunc(rows, func(r render.SheetRow) bool { return r.TaskID == id })
	})
}

// --- logging and editing entries ------------------------------------------------------------

// LogTime logs time on the current task with one line, e.g. "1h30 fixed login".
func (a *App) LogTime() {
	t := a.current()
	if t == nil {
		return
	}
	task := &clickup.EntryTask{ID: clickup.FlexString(t.ID), CustomID: t.CustomID, Name: t.Name, Status: t.Status}
	a.logPrompt("Log time on "+t.Label(), task, time.Time{})
}

// logPrompt asks for "<duration> [day] [HH:MM] [note]"; day is the default day (zero: ending now).
func (a *App) logPrompt(title string, task *clickup.EntryTask, day time.Time) {
	a.UI.Prompt(Prompt{Title: title, Hint: logHint}, func(answer string) {
		spec, err := parse.Time(answer, a.Now())
		switch {
		case err != nil:
			a.UI.Notify(Error, err.Error())
		case spec.Duration > 0:
			a.createEntry(task, spec.Start(day, a.Now()), spec.Duration, spec.Note)
		}
	})
}

// editEntry edits one entry with the same one-line format; empty or 0 deletes it.
func (a *App) editEntry(e clickup.TimeEntry, title string) {
	if e.Running() {
		a.UI.Notify(Warn, "That's the running timer. Stop it first (T), then edit it.")
		return
	}
	start := e.StartTime()
	value := strings.TrimSpace(parse.Hours(e.Length(a.Now())) + " " + start.Format("15:04") + " " + e.Description)
	a.UI.Prompt(Prompt{Title: title, Value: value, Hint: "1:30 · 09:00 · note · a day moves it · empty or 0 deletes", AllowEmpty: true},
		func(answer string) {
			if strings.TrimSpace(answer) == "" {
				a.deleteEntry(e)
				return
			}
			spec, err := parse.Time(answer, a.Now())
			if err != nil {
				a.UI.Notify(Error, err.Error())
				return
			}
			if spec.Duration == 0 {
				a.deleteEntry(e)
				return
			}
			newStart := start
			day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
			if !spec.Day.IsZero() {
				newStart = spec.Day.Add(start.Sub(day)) // same time of day, other day
			}
			if spec.HasClock {
				newStart = time.Date(newStart.Year(), newStart.Month(), newStart.Day(), 0, 0, 0, 0, newStart.Location()).Add(spec.Clock)
			}
			a.updateEntry(e, newStart, spec.Duration, spec.Note)
		})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func entryFor(task *clickup.EntryTask, start time.Time, d time.Duration, note string) clickup.TimeEntry {
	return clickup.TimeEntry{
		Task: task, Description: note,
		Start:    clickup.FlexString(strconv.FormatInt(start.UnixMilli(), 10)),
		End:      clickup.FlexString(strconv.FormatInt(start.Add(d).UnixMilli(), 10)),
		Duration: clickup.FlexString(strconv.FormatInt(d.Milliseconds(), 10)),
	}
}

func change(e clickup.TimeEntry) clickup.EntryChange {
	return clickup.EntryChange{
		TaskID: e.TaskID(), Start: e.Start.Int(), End: e.End.Int(), Duration: e.Duration.Int(), Description: e.Description,
	}
}

// inWeek reports whether e belongs on the sheet's current week.
func (a *App) inWeek(e clickup.TimeEntry) bool {
	return !a.Sheet.Week.IsZero() && render.DayOfWeek(e.StartTime(), a.Sheet.Week) >= 0
}

func (a *App) indexEntry(id clickup.FlexString) int {
	return slices.IndexFunc(a.Sheet.Entries, func(e clickup.TimeEntry) bool { return e.ID == id })
}

func (a *App) persistWeek() {
	if a.Sheet.Week.IsZero() {
		return
	}
	saved := slices.DeleteFunc(slices.Clone(a.Sheet.Entries), func(e clickup.TimeEntry) bool { return strings.HasPrefix(string(e.ID), "tmp-") })
	_ = cache.Put(a.Cache, a.weekKey(a.Sheet.Week), saved)
}

// afterTimeChange refreshes the task panel's tracked time.
func (a *App) afterTimeChange(taskID string) {
	a.persistWeek()
	if a.Detail != nil && a.Detail.ID == taskID && !a.Detail.Pending {
		a.LoadDetail(a.Detail)
	}
}

func (a *App) createEntry(task *clickup.EntryTask, start time.Time, d time.Duration, note string) {
	e := entryFor(task, start, d, note)
	e.ID = clickup.FlexString(fmt.Sprintf("tmp-%d", a.Now().UnixNano()))
	e.User = a.Me
	if a.inWeek(e) {
		a.Sheet.Entries = append(a.Sheet.Entries, e)
	}
	label := cmp.Or(task.CustomID, string(task.ID))
	a.log(fmt.Sprintf("Log %s on %s (%s)", parse.Hours(d), label, start.Format("Mon 02 15:04")))
	teamID := a.TeamID
	a.run("", func(ctx context.Context, apply func(func())) {
		created, err := a.API.CreateTimeEntry(ctx, teamID, change(e))
		apply(func() {
			i := a.indexEntry(e.ID)
			if err != nil {
				if i >= 0 {
					a.Sheet.Entries = slices.Delete(a.Sheet.Entries, i, i+1)
				}
				a.error("Logging time failed", err)
				return
			}
			if i >= 0 {
				// The create response can be sparse: keep what we know.
				if created.Task == nil {
					created.Task = task
				}
				created.Start = cmp.Or(created.Start, e.Start)
				created.Duration = cmp.Or(created.Duration, e.Duration)
				created.Description = cmp.Or(created.Description, e.Description)
				a.Sheet.Entries[i] = created
			}
			a.UI.Notify(Info, "Logged "+parse.Hours(d)+" on "+label)
			a.afterTimeChange(string(task.ID))
		})
	})
}

func (a *App) updateEntry(old clickup.TimeEntry, start time.Time, d time.Duration, note string) {
	e := entryFor(old.Task, start, d, note)
	e.ID, e.User, e.Location, e.TaskURL = old.ID, old.User, old.Location, old.TaskURL
	i := a.indexEntry(old.ID)
	switch {
	case i >= 0 && a.inWeek(e):
		a.Sheet.Entries[i] = e
	case i >= 0:
		a.Sheet.Entries = slices.Delete(a.Sheet.Entries, i, i+1) // moved out of this week
	case a.inWeek(e):
		a.Sheet.Entries = append(a.Sheet.Entries, e)
	}
	a.log(fmt.Sprintf("Time entry → %s (%s)", parse.Hours(d), start.Format("Mon 02 15:04")))
	teamID, id := a.TeamID, string(old.ID)
	a.run("", func(ctx context.Context, apply func(func())) {
		err := a.API.UpdateTimeEntry(ctx, teamID, id, change(e))
		apply(func() {
			if err != nil {
				if j := a.indexEntry(old.ID); j >= 0 {
					a.Sheet.Entries = slices.Delete(a.Sheet.Entries, j, j+1)
				}
				if a.inWeek(old) {
					a.Sheet.Entries = append(a.Sheet.Entries, old)
				}
				a.error("Updating time failed, reverted", err)
				return
			}
			a.afterTimeChange(old.TaskID())
		})
	})
}

func (a *App) deleteEntry(e clickup.TimeEntry) {
	if i := a.indexEntry(e.ID); i >= 0 {
		a.Sheet.Entries = slices.Delete(slices.Clone(a.Sheet.Entries), i, i+1)
	}
	a.log(fmt.Sprintf("Delete time entry %s (%s)", parse.Hours(e.Length(a.Now())), e.StartTime().Format("Mon 02 15:04")))
	teamID, id := a.TeamID, string(e.ID)
	a.run("", func(ctx context.Context, apply func(func())) {
		err := a.API.DeleteTimeEntry(ctx, teamID, id)
		apply(func() {
			if err != nil {
				if a.inWeek(e) {
					a.Sheet.Entries = append(a.Sheet.Entries, e)
				}
				a.error("Deleting time failed, restored", err)
				return
			}
			a.afterTimeChange(e.TaskID())
		})
	})
}

// TaskTime lists the current task's time entries (last year) to edit, or logs new time.
func (a *App) TaskTime() {
	t := a.current()
	if t == nil {
		return
	}
	id, label, teamID, now := t.ID, t.Label(), a.TeamID, a.Now()
	task := &clickup.EntryTask{ID: clickup.FlexString(t.ID), CustomID: t.CustomID, Name: t.Name}
	a.run("tasktime", func(ctx context.Context, apply func(func())) {
		entries, err := a.API.TimeEntries(ctx, teamID, now.AddDate(-1, 0, 0), now.AddDate(0, 0, 1), id)
		apply(func() {
			if err != nil {
				a.error("Loading time entries", err)
				return
			}
			if len(entries) == 0 {
				a.logPrompt("Log time on "+label+" (none logged yet)", task, time.Time{})
				return
			}
			slices.SortFunc(entries, func(x, y clickup.TimeEntry) int { return cmp.Compare(y.Start.Int(), x.Start.Int()) })
			var total time.Duration
			for _, e := range entries {
				total += e.Length(a.Now())
			}
			items := []MenuItem{{Key: 'a', Label: style.Green("+ log time"), Value: nil}}
			for i, e := range entries[:min(len(entries), len(optionKeys))] {
				items = append(items, MenuItem{Key: optionKeys[i], Label: render.EntryLine(e, a.Now(), true), Value: e})
			}
			a.UI.Menu(fmt.Sprintf("Time on %s · %s total", label, parse.Hours(total)), items, 1, func(item MenuItem) {
				if e, ok := item.Value.(clickup.TimeEntry); ok {
					a.editEntry(e, "Time entry · "+label)
				} else {
					a.logPrompt("Log time on "+label, task, time.Time{})
				}
			})
		})
	})
}

// --- timer -----------------------------------------------------------------------------------

// LoadTimer fetches the running timer (it may have been started elsewhere).
func (a *App) LoadTimer() {
	teamID := a.TeamID
	a.run("timer", func(ctx context.Context, apply func(func())) {
		current, err := a.API.CurrentTimer(ctx, teamID)
		apply(func() {
			if err == nil {
				a.setTimer(current)
			}
		})
	})
}

// ToggleTimer starts a timer on the current task, or stops it when it runs on this task.
// Starting one stops any other running timer, as in ClickUp.
func (a *App) ToggleTimer() {
	t := a.current()
	if t == nil {
		return
	}
	teamID, previous := a.TeamID, a.Timer
	if a.Timer != nil && a.Timer.TaskID() == t.ID {
		a.setTimer(nil)
		a.log("Stop timer · " + t.Label())
		a.run("", func(ctx context.Context, apply func(func())) {
			stopped, err := a.API.StopTimer(ctx, teamID)
			apply(func() {
				if err != nil {
					a.setTimer(previous)
					a.error("Stopping the timer failed", err)
					return
				}
				length := time.Duration(stopped.Duration.Int()) * time.Millisecond
				if length <= 0 {
					length = a.Now().Sub(previous.StartTime())
				}
				a.UI.Notify(Info, "Stopped: "+parse.Hours(length)+" on "+t.Label())
				if !a.Sheet.Week.IsZero() {
					a.LoadWeek()
				}
				a.afterTimeChange(t.ID)
			})
		})
		return
	}
	now := a.Now()
	running := clickup.TimeEntry{
		Task:     &clickup.EntryTask{ID: clickup.FlexString(t.ID), CustomID: t.CustomID, Name: t.Name},
		Start:    clickup.FlexString(strconv.FormatInt(now.UnixMilli(), 10)),
		Duration: clickup.FlexString(strconv.FormatInt(-now.UnixMilli(), 10)),
	}
	a.setTimer(&running)
	a.log("Start timer · " + t.Label())
	a.run("", func(ctx context.Context, apply func(func())) {
		started, err := a.API.StartTimer(ctx, teamID, t.ID)
		apply(func() {
			if err != nil {
				a.setTimer(previous)
				a.error("Starting the timer failed", err)
				return
			}
			if started.Task == nil {
				started.Task = running.Task
			}
			started.Start = cmp.Or(started.Start, running.Start)
			started.Duration = cmp.Or(started.Duration, running.Duration)
			a.setTimer(&started)
			if previous != nil && !a.Sheet.Week.IsZero() {
				a.LoadWeek() // the previous timer became an entry
			}
		})
	})
}

// TimerLine is the running timer for the status panel, e.g. "⏱ 0:23:05 DEV-1 Fix login".
func (a *App) TimerLine() string {
	if a.Timer == nil {
		return ""
	}
	d := a.Now().Sub(a.Timer.StartTime()).Round(time.Second)
	clock := fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	name := ""
	if a.Timer.Task != nil {
		name = cmp.Or(a.Timer.Task.CustomID, string(a.Timer.Task.ID)) + " " + a.Timer.Task.Name
	}
	return style.Green("⏱ "+clock) + " " + name
}

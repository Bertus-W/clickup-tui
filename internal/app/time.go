package app

import (
	"cmp"
	"context"
	"errors"
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

func (a *App) weekKey(week time.Time) string {
	return "time:" + a.TeamID + ":" + week.Format(time.DateOnly)
}

// extraKey stores the rows added to a week that have no time yet, so they survive paging
// through weeks and restarts.
func (a *App) extraKey(week time.Time) string {
	return "sheetrows:" + a.TeamID + ":" + week.Format(time.DateOnly)
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
		a.Sheet.Extra = cache.Value[[]render.SheetTask](a.Cache, a.extraKey(a.Sheet.Week), nil)
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
	a.Sheet.Extra, a.Sheet.Row = cache.Value[[]render.SheetTask](a.Cache, a.extraKey(a.Sheet.Week), nil), 0
	a.LoadWeek()
}

// LoadWeek shows the cached week at once, then refreshes it.
func (a *App) LoadWeek() {
	week, teamID := a.Sheet.Week, a.TeamID
	cached, _, hit := cache.Get[[]clickup.TimeEntry](a.Cache, a.weekKey(week))
	a.Sheet.Entries, a.Sheet.Loading = a.keepPendingEntries(cached), !hit
	a.clampSheet()
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
			a.Sheet.Entries = a.keepPendingEntries(entries)
			a.clampSheet()
		})
	})
}

// keepPendingEntries adjusts a freshly loaded week: entries still being saved stay, entries
// being deleted stay gone.
func (a *App) keepPendingEntries(entries []clickup.TimeEntry) []clickup.TimeEntry {
	entries = slices.DeleteFunc(slices.Clone(entries), func(e clickup.TimeEntry) bool { return a.deleting[e.ID] })
	for _, e := range a.Sheet.Entries {
		if pendingEntry(e) && a.inWeek(e) {
			entries = append(entries, e)
		}
	}
	return entries
}

func pendingEntry(e clickup.TimeEntry) bool { return strings.HasPrefix(string(e.ID), "tmp-") }

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

// AddToCell logs a new entry on the selected day and task, whatever the cell already holds.
func (a *App) AddToCell() {
	row, day, ok := a.sheetCell()
	if !ok {
		a.UI.Notify(Warn, "No tasks this week yet. Press n to add one, or L on a task to log time.")
		return
	}
	task := &clickup.EntryTask{ID: clickup.FlexString(row.TaskID), CustomID: row.Label, Name: row.Name}
	// Start where the day's last entry ends, or at 09:00.
	start := 9 * time.Hour
	for _, e := range a.SelectedEntries() {
		start = max(start, parse.ClockOf(e.StartTime().Add(e.Length(a.Now()))))
	}
	a.entryForm("Add time · "+day.Format("Mon 02 Jan")+" · "+row.Name, day, start, true, 0, "",
		func(start time.Time, d time.Duration, note string) { a.createEntry(task, start, d, note) })
}

// EditCellEntry edits or deletes one entry of the selected cell: pick the entry (skipped when
// there is only one), then edit or delete it.
func (a *App) EditCellEntry() {
	row, day, ok := a.sheetCell()
	entries := a.SelectedEntries()
	if !ok || len(entries) == 0 {
		a.UI.Notify(Warn, "No time on this day to edit. enter adds some.")
		return
	}
	title := day.Format("Mon 02 Jan") + " · " + row.Name
	if len(entries) == 1 {
		a.entryActions(entries[0], title)
		return
	}
	items := make([]MenuItem, 0, len(entries))
	for i, e := range entries[:min(len(entries), len(optionKeys))] {
		items = append(items, MenuItem{Key: optionKeys[i], Label: render.EntryLine(e, a.Now(), false), Value: e})
	}
	a.UI.Menu("Which entry? · "+title, items, 0, func(item MenuItem) {
		a.entryActions(item.Value.(clickup.TimeEntry), title)
	})
}

// entryActions offers editing or deleting one entry.
func (a *App) entryActions(e clickup.TimeEntry, title string) {
	if pendingEntry(e) {
		a.UI.Notify(Warn, "That entry is still being saved. Try again in a moment.")
		return
	}
	items := []MenuItem{
		{Key: 'e', Label: "Edit  " + style.Dim(render.EntryLine(e, a.Now(), false)), Value: "edit"},
		{Key: 'd', Label: style.Red("Delete"), Value: "delete"},
	}
	a.UI.Menu(title, items, 0, func(item MenuItem) {
		if item.Value == "delete" {
			msg := fmt.Sprintf("Delete %s on %s?", render.EntryLine(e, a.Now(), false), e.StartTime().Format("Mon 02 Jan"))
			a.UI.Confirm("Delete time entry", msg, func() { a.deleteEntry(e) })
		} else {
			a.editEntry(e, title)
		}
	})
}

// ClearCell deletes every entry in the selected cell.
func (a *App) ClearCell() {
	row, day, ok := a.sheetCell()
	entries := a.SelectedEntries()
	if !ok || len(entries) == 0 {
		return
	}
	if slices.ContainsFunc(entries, pendingEntry) {
		a.UI.Notify(Warn, "An entry on that day is still being saved. Try again in a moment.")
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

// AddSheetTask adds a task row to the sheet. It offers pinned tasks, the open list, my
// tasks and last week's rows; any other task can be added by id or URL.
func (a *App) AddSheetTask() {
	const byRef = "\x00ref"
	seen := map[string]bool{}
	options := []Option{{ID: byRef, Label: style.Green("+ another task by id or URL…")}}
	rows := map[string]render.SheetTask{}
	add := func(r render.SheetTask) {
		if r.ID == "" || seen[r.ID] {
			return
		}
		seen[r.ID], rows[r.ID] = true, r
		options = append(options, Option{ID: r.ID, Label: style.Dim(r.Label) + " " + r.Name + style.Dim("  "+r.List)})
	}
	mine := cache.Value[[]*clickup.Task](a.Cache, fmt.Sprintf("tasks:%s:my::false", a.TeamID), nil)
	for _, t := range slices.Concat(a.Pinned, []*clickup.Task{a.Detail}, a.Tasks, mine) {
		if t != nil && !t.Pending {
			add(sheetTask(t))
		}
	}
	lastWeek := a.Sheet.Week.AddDate(0, 0, -7)
	prev, _, _ := render.Sheet(cache.Value[[]clickup.TimeEntry](a.Cache, a.weekKey(lastWeek), nil), lastWeek,
		cache.Value[[]render.SheetTask](a.Cache, a.extraKey(lastWeek), nil), a.Now())
	for _, r := range prev {
		add(render.SheetTask{ID: r.TaskID, Label: r.Label, Name: r.Name, List: r.List})
	}
	a.UI.Pick("Add a task row · type to search", options, "", func(id string) {
		if id != byRef {
			a.addSheetRow(rows[id])
			return
		}
		check := func(ref string) error {
			if parse.TaskRef(ref) == "" {
				return errors.New("that URL doesn't point at a task")
			}
			return nil
		}
		a.UI.Prompt(Prompt{Title: "Add a task row", Placeholder: "task id, custom id (ABC-123) or URL", Check: check}, func(ref string) {
			id, teamID, week := parse.TaskRef(ref), a.TeamID, a.Sheet.Week
			a.run("", func(ctx context.Context, apply func(func())) {
				t, err := a.API.GetTask(ctx, id, teamID)
				apply(func() {
					switch {
					case err != nil:
						a.error("Task "+id, err)
					case a.Sheet.Week.Equal(week):
						a.addSheetRow(sheetTask(&t))
					}
				})
			})
		})
	})
}

func sheetTask(t *clickup.Task) render.SheetTask {
	return render.SheetTask{ID: t.ID, Label: t.Label(), Name: t.Name, List: t.List.Name}
}

// addSheetRow shows a task on this week's sheet (remembered for the week) and selects it.
func (a *App) addSheetRow(t render.SheetTask) {
	rows, _, _ := a.SheetRows()
	if !slices.ContainsFunc(rows, func(r render.SheetRow) bool { return r.TaskID == t.ID }) {
		a.Sheet.Extra = append(slices.Clone(a.Sheet.Extra), t)
		_ = cache.Put(a.Cache, a.extraKey(a.Sheet.Week), a.Sheet.Extra)
	}
	rows, _, _ = a.SheetRows()
	a.Sheet.Row = slices.IndexFunc(rows, func(r render.SheetRow) bool { return r.TaskID == t.ID })
}

// --- logging and editing entries ------------------------------------------------------------

// LogTime logs time on the current task in the entry form. Typing a duration and enter
// logs time that ends now; the one-line form works too: "1h30 yesterday fixed login".
func (a *App) LogTime() {
	t := a.current()
	if t == nil {
		return
	}
	task := &clickup.EntryTask{ID: clickup.FlexString(t.ID), CustomID: t.CustomID, Name: t.Name, Status: t.Status}
	a.logForm("Log time · "+t.Label(), task)
}

func (a *App) logForm(title string, task *clickup.EntryTask) {
	a.entryForm(title, midnight(a.Now()), 0, false, 0, "",
		func(start time.Time, d time.Duration, note string) { a.createEntry(task, start, d, note) })
}

// editEntry edits one entry in the entry form.
func (a *App) editEntry(e clickup.TimeEntry, title string) {
	if e.Running() {
		a.UI.Notify(Warn, "That's the running timer. Stop it first (T), then edit it.")
		return
	}
	start := e.StartTime()
	a.entryForm("Edit time · "+title, midnight(start), parse.ClockOf(start), true, e.Length(a.Now()), e.Description,
		func(start time.Time, d time.Duration, note string) { a.updateEntry(e, start, d, note) })
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func clockText(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// entryForm edits a time entry as separate fields: the duration in the input on top, then the
// day, the start, the end (calculated live from start and duration) and a note. Setting the
// end recalculates the duration. Without a start (startSet false) the entry ends now.
//
// The input also takes the one-line form, "45m fixed login" or "1h30 yesterday 13:00 review":
// its day, time and note fill in the rows below.
func (a *App) entryForm(title string, day time.Time, start time.Duration, startSet bool, duration time.Duration, note string, save func(time.Time, time.Duration, string)) {
	f := &Form{
		Title:    title,
		Hint:     "e.g. 1h30 or 45m fixed login · enter: save · ↓: more",
		ListHint: "enter: edit · ↑: duration · ctrl+s: save",
	}
	if duration > 0 {
		f.Name = parse.Hours(duration)
	}
	type values struct {
		day      time.Time
		start    time.Duration
		d        time.Duration
		note     string
		endsNow  bool
		fromLine bool // the input held more than a duration
		err      error
	}
	// resolve combines the rows with what the input says.
	resolve := func() values {
		v := values{day: day, start: start, note: note}
		d, ok := parse.Duration(f.Name)
		var spec parse.TimeSpec
		if !ok {
			var err error
			if spec, err = parse.Time(f.Name, a.Now()); err != nil {
				v.err = err
				return v
			}
			d, v.fromLine = spec.Duration, true
		}
		if d <= 0 {
			v.err = errors.New("give a duration above zero, like 1:30, 1h30 or 90m")
			return v
		}
		v.d = d
		if !spec.Day.IsZero() {
			v.day = spec.Day
		}
		if spec.Note != "" {
			v.note = spec.Note
		}
		switch {
		case spec.HasClock:
			v.start = spec.Clock
		case startSet:
		case !spec.Day.IsZero() && !spec.Day.Equal(midnight(a.Now())):
			v.start = 9 * time.Hour
		default: // it ends now
			begin := a.Now().Add(-d)
			v.day, v.start, v.endsNow = midnight(begin), parse.ClockOf(begin), true
		}
		return v
	}
	// settle turns the input into rows before a row is edited, so the two can't disagree.
	settle := func() {
		if v := resolve(); v.err == nil {
			day, start, note, f.Name = v.day, v.start, v.note, parse.Hours(v.d)
			startSet = startSet || !v.endsNow
		}
	}
	checkClock := func(answer string) error {
		if _, ok := parse.Clock(answer); !ok {
			return fmt.Errorf("%q isn't a time of day", answer)
		}
		return nil
	}
	f.Rows = func() []FormRow {
		v := resolve()
		startText := clockText(v.start)
		end := style.Dim("type a duration above")
		endClock := v.start + time.Hour
		if v.err == nil {
			endClock = v.start + v.d
			endAt := parse.At(v.day, v.start).Add(v.d)
			end = endAt.Format("15:04") + style.Dim("  calculated")
			if endAt.Day() != v.day.Day() {
				end = endAt.Format("15:04") + style.Dim("  next day, calculated")
			}
			if v.endsNow {
				startText += style.Dim("  calculated")
				end = endAt.Format("15:04") + style.Dim("  now")
			}
		} else if !startSet {
			startText = style.Dim("so that it ends now")
			end = style.Dim("now")
		}
		return []FormRow{
			{Label: "Day", Value: v.day.Format("Mon 02 Jan 2006"), Edit: func(done func()) {
				settle()
				check := func(answer string) error {
					if _, ok := parse.Day(answer, a.Now()); !ok {
						return fmt.Errorf("%q isn't a past day", answer)
					}
					return nil
				}
				a.UI.Prompt(Prompt{Title: "Day", Value: day.Format(time.DateOnly), Hint: "today · yesterday · mon · 21-09 · 2026-09-21", Check: check},
					func(answer string) {
						day, _ = parse.Day(answer, a.Now())
						if !startSet { // a day in the past doesn't end now: start at the usual time
							start, startSet = 9*time.Hour, true
						}
						done()
					})
			}},
			{Label: "Start", Value: startText, Edit: func(done func()) {
				settle()
				a.UI.Prompt(Prompt{Title: "Start", Value: clockText(start), Hint: "time of day: 09:00 · 13:30", Check: checkClock},
					func(answer string) {
						start, _ = parse.Clock(answer)
						startSet = true
						done()
					})
			}},
			{Label: "End", Value: end, Edit: func(done func()) {
				settle()
				startSet = true
				check := func(answer string) error {
					if err := checkClock(answer); err != nil {
						return err
					}
					if c, _ := parse.Clock(answer); c <= start {
						return errors.New("the end must be after the start (" + clockText(start) + ")")
					}
					return nil
				}
				a.UI.Prompt(Prompt{Title: "End", Value: clockText(endClock % (24 * time.Hour)), Hint: "time of day: 17:00", Check: check},
					func(answer string) {
						c, _ := parse.Clock(answer)
						f.Name = parse.Hours(c - start) // the duration follows the end
						done()
					})
			}},
			{Label: "Note", Value: cmp.Or(v.note, style.Dim("—")), Edit: func(done func()) {
				settle()
				a.UI.Prompt(Prompt{Title: "Note", Value: note, AllowEmpty: true}, func(answer string) {
					note = answer
					done()
				})
			}},
		}
	}
	f.Submit = func(name string) (int, bool) {
		f.Name = name
		v := resolve()
		if v.err != nil {
			f.Error = v.err.Error()
			return -1, false
		}
		if at := parse.At(v.day, v.start); at.After(a.Now()) {
			f.Error = "that starts in the future: time can only be logged for the past"
			return -1, false
		}
		save(parse.At(v.day, v.start), v.d, v.note)
		return 0, true
	}
	a.UI.Form(f)
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
		Billable: e.Billable,
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
		a.ReloadDetail(a.Detail) // the tracked time changed, which the list can't tell
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
				a.clampSheet()
				a.error("Logging time failed", err)
				return
			}
			// The create response can be sparse: keep what we know.
			if created.Task == nil {
				created.Task = task
			}
			created.Start = cmp.Or(created.Start, e.Start)
			created.Duration = cmp.Or(created.Duration, e.Duration)
			created.Description = cmp.Or(created.Description, e.Description)
			switch {
			case i >= 0:
				a.Sheet.Entries[i] = created
			case a.inWeek(created) && a.indexEntry(created.ID) < 0: // a reload dropped the placeholder
				a.Sheet.Entries = append(a.Sheet.Entries, created)
			}
			a.clampSheet()
			a.UI.Notify(Info, "Logged "+parse.Hours(d)+" on "+label)
			a.afterTimeChange(string(task.ID))
		})
	})
}

func (a *App) updateEntry(old clickup.TimeEntry, start time.Time, d time.Duration, note string) {
	e := entryFor(old.Task, start, d, note)
	e.ID, e.User, e.Location, e.TaskURL, e.Billable = old.ID, old.User, old.Location, old.TaskURL, old.Billable
	i := a.indexEntry(old.ID)
	switch {
	case i >= 0 && a.inWeek(e):
		a.Sheet.Entries[i] = e
	case i >= 0:
		a.Sheet.Entries = slices.Delete(a.Sheet.Entries, i, i+1) // moved out of this week
	case a.inWeek(e):
		a.Sheet.Entries = append(a.Sheet.Entries, e)
	}
	a.clampSheet()
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
				a.clampSheet()
				a.error("Updating time failed, reverted", err)
				return
			}
			a.afterTimeChange(old.TaskID())
		})
	})
}

func (a *App) deleteEntry(e clickup.TimeEntry) {
	if pendingEntry(e) {
		a.UI.Notify(Warn, "That entry is still being saved. Try again in a moment.")
		return
	}
	if i := a.indexEntry(e.ID); i >= 0 {
		a.Sheet.Entries = slices.Delete(slices.Clone(a.Sheet.Entries), i, i+1)
	}
	a.deleting[e.ID] = true
	a.clampSheet()
	a.log(fmt.Sprintf("Delete time entry %s (%s)", parse.Hours(e.Length(a.Now())), e.StartTime().Format("Mon 02 15:04")))
	teamID, id := a.TeamID, string(e.ID)
	a.run("", func(ctx context.Context, apply func(func())) {
		err := a.API.DeleteTimeEntry(ctx, teamID, id)
		apply(func() {
			delete(a.deleting, e.ID)
			if err != nil {
				if a.inWeek(e) && a.indexEntry(e.ID) < 0 {
					a.Sheet.Entries = append(a.Sheet.Entries, e)
				}
				a.clampSheet()
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
				a.logForm("Log time · "+label+" (none logged yet)", task)
				return
			}
			slices.SortFunc(entries, func(x, y clickup.TimeEntry) int { return cmp.Compare(y.Start.Int(), x.Start.Int()) })
			var total time.Duration
			for _, e := range entries {
				total += e.Length(a.Now())
			}
			items := []MenuItem{{Key: '+', Label: style.Green("+ log time"), Value: nil}}
			for i, e := range entries[:min(len(entries), len(optionKeys))] {
				items = append(items, MenuItem{Key: optionKeys[i], Label: render.EntryLine(e, a.Now(), true), Value: e})
			}
			a.UI.Menu(fmt.Sprintf("Time on %s · %s total", label, parse.Hours(total)), items, 1, func(item MenuItem) {
				if e, ok := item.Value.(clickup.TimeEntry); ok {
					a.entryActions(e, "Time entry · "+label)
				} else {
					a.logForm("Log time · "+label, task)
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
			switch {
			case a.TeamID != teamID:
			case err != nil:
				a.log(style.Dim("Checking for a running timer: " + err.Error()))
			default:
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
	a.seq["timer"]++ // a timer lookup still in flight is older than this and gets dropped
	if a.Timer != nil && a.Timer.TaskID() == t.ID {
		a.setTimer(nil)
		a.log("Stop timer · " + t.Label())
		a.run("", func(ctx context.Context, apply func(func())) {
			stopped, err := a.API.StopTimer(ctx, teamID)
			apply(func() {
				if a.TeamID != teamID {
					return
				}
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
			if a.TeamID != teamID {
				return
			}
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

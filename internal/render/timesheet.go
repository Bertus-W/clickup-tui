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

// SheetRow is one task in the weekly timesheet.
type SheetRow struct {
	TaskID, Label, Name, List string
	Days                      [7]time.Duration
	Entries                   [7][]clickup.TimeEntry
	Total                     time.Duration
}

// SheetTask is a task on the sheet without time yet (added with `a`).
type SheetTask struct {
	ID, Label, Name, List string
}

// Sheet groups a week's entries by task and day (Monday first). extra adds empty rows.
func Sheet(entries []clickup.TimeEntry, week time.Time, extra []SheetTask, now time.Time) (rows []SheetRow, days [7]time.Duration, total time.Duration) {
	byTask := map[string]*SheetRow{}
	row := func(id, label, name, list string) *SheetRow {
		if r, ok := byTask[id]; ok {
			return r
		}
		r := &SheetRow{TaskID: id, Label: label, Name: name, List: list}
		byTask[id] = r
		return r
	}
	for _, e := range entries {
		day := DayOfWeek(e.StartTime(), week)
		if day < 0 {
			continue
		}
		name, label := "(no task)", ""
		if e.Task != nil {
			name, label = e.Task.Name, cmp.Or(e.Task.CustomID, string(e.Task.ID))
		}
		r := row(e.TaskID(), label, name, e.Location.ListName)
		d := e.Length(now)
		r.Days[day] += d
		r.Entries[day] = append(r.Entries[day], e)
		r.Total += d
		days[day] += d
		total += d
	}
	for _, t := range extra {
		row(t.ID, t.Label, t.Name, t.List)
	}
	for _, r := range byTask {
		rows = append(rows, *r)
		for d := range 7 {
			slices.SortFunc(rows[len(rows)-1].Entries[d], func(a, b clickup.TimeEntry) int {
				return cmp.Compare(a.Start.Int(), b.Start.Int())
			})
		}
	}
	slices.SortFunc(rows, func(a, b SheetRow) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.TaskID, b.TaskID))
	})
	return rows, days, total
}

const sheetCell = 7 // "Mon 22" / " 1:30 "

// DayOfWeek is t's column in the week starting at week (Monday 00:00), or -1. Calendar days,
// not 24-hour blocks, so daylight saving changes don't shift entries.
func DayOfWeek(t, week time.Time) int {
	for d := range 7 {
		if !t.Before(week.AddDate(0, 0, d)) && t.Before(week.AddDate(0, 0, d+1)) {
			return d
		}
	}
	return -1
}

// WeekStart is the Monday 00:00 of t's week.
func WeekStart(t time.Time) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// SheetLines renders the grid: a header, one line per task and a totals line. The selected
// cell is bracketed, today's column is highlighted.
func sheetNameWidth(width int) int { return max(16, width-8*(sheetCell+1)-2) }

// SheetColumnAt is the day column (0–6) at x in a sheet of the given width, or -1.
func SheetColumnAt(width, x int) int {
	d := (x - sheetNameWidth(width) - 1) / (sheetCell + 1)
	if x <= sheetNameWidth(width) || d > 6 {
		return -1
	}
	return d
}

func SheetLines(rows []SheetRow, days [7]time.Duration, total time.Duration, week time.Time, width, selRow, selCol int, now time.Time) (header string, lines []string, footer string) {
	nameW := sheetNameWidth(width)
	today := DayOfWeek(now, week)
	cell := func(text string, selected bool, st style.Style) string {
		if selected {
			return style.Cell{Text: "[" + text + "]"}.Fit(sheetCell)
		}
		return style.Cell{Text: " " + text, Style: st}.Fit(sheetCell)
	}
	hours := func(d time.Duration) string {
		if d == 0 {
			return "  ·"
		}
		return strings.Repeat(" ", max(0, 5-len(parse.Hours(d)))) + parse.Hours(d)
	}

	var h strings.Builder
	h.WriteString(style.Bold(style.Cell{Text: "Task"}.Fit(nameW)))
	for d := range 7 {
		label := week.AddDate(0, 0, d).Format("Mon 02")
		st := style.Bold
		if d == today {
			st = style.Yellow
		}
		h.WriteString(" " + st(style.Cell{Text: label}.Fit(sheetCell)))
	}
	h.WriteString(" " + style.Bold(style.Cell{Text: "  Total"}.Fit(sheetCell)))
	header = h.String()

	for i, r := range rows {
		var b strings.Builder
		name := r.Name
		if r.Label != "" {
			name = r.Label + " " + name
		}
		b.WriteString(style.Cell{Text: name}.Fit(nameW))
		for d := range 7 {
			st := style.Plain
			if r.Days[d] == 0 {
				st = style.Dim
			}
			b.WriteString(" " + cell(hours(r.Days[d]), i == selRow && d == selCol, st))
		}
		b.WriteString(" " + style.Bold(style.Cell{Text: " " + hours(r.Total)}.Fit(sheetCell)))
		lines = append(lines, b.String())
	}

	var f strings.Builder
	f.WriteString(style.Bold(style.Cell{Text: "Total"}.Fit(nameW)))
	for d := range 7 {
		f.WriteString(" " + style.Bold(style.Cell{Text: " " + hours(days[d])}.Fit(sheetCell)))
	}
	f.WriteString(" " + style.BoldCyan(style.Cell{Text: " " + hours(total)}.Fit(sheetCell)))
	return header, lines, f.String()
}

// EntryLine renders one time entry: "Mon 22 09:00–10:30  1:30  note".
func EntryLine(e clickup.TimeEntry, now time.Time, withDay bool) string {
	start := e.StartTime()
	span := start.Format("15:04") + "–"
	if e.Running() {
		span += style.Green("now")
	} else {
		span += start.Add(e.Length(now)).Format("15:04")
	}
	if withDay {
		span = start.Format("Mon 02 Jan ") + span
	}
	line := span + "  " + style.Bold(parse.Hours(e.Length(now)))
	if e.Description != "" {
		line += "  " + e.Description
	}
	return line
}

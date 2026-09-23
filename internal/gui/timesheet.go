package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

const (
	viewSheet     = "sheet"
	viewSheetSide = "sheetSide"
)

// The timesheet is a page of its own (T), like ClickUp's web Timesheet: a week of my time
// per task and day, with the entries behind the selected cell on the right.

func (gui *Gui) openSheet() {
	gui.sheetPrev = gui.panel
	gui.sheetOpen = true
	gui.panel = viewSheet
	gui.filtering = false
	gui.App.OpenTimesheet()
}

func (gui *Gui) closeSheet() {
	gui.sheetOpen = false
	gui.panel = gui.sheetPrev
}

func (gui *Gui) sheetKeys() []binding {
	a := func(fn func(*app.App)) func() error { return func() error { fn(gui.App); return nil } }
	sheet := []string{viewSheet}
	return []binding{
		{sheet, []any{gocui.KeyEnter}, "Add time", true, false, a((*app.App).AddToCell)},
		{sheet, []any{'e'}, "Edit/delete entry", true, false, a((*app.App).EditCellEntry)},
		{sheet, []any{'n'}, "Add task row", true, false, a((*app.App).AddSheetTask)},
		{sheet, []any{'d'}, "Clear day", false, false, a((*app.App).ClearCell)},
		{sheet, []any{'['}, "Previous week", true, false, a(func(x *app.App) { x.ShiftWeek(-1) })},
		{sheet, []any{']'}, "Next week", true, false, a(func(x *app.App) { x.ShiftWeek(1) })},
		{sheet, []any{'w'}, "This week", false, false, a(func(x *app.App) { x.ShiftWeek(0) })},
		{sheet, []any{'R'}, "Refresh", false, false, a((*app.App).LoadWeek)},
		{sheet, []any{gocui.KeyEsc, gocui.KeyF1}, "Back to tasks", true, false, do(gui.closeSheet)},
		{sheet, []any{'h', gocui.KeyArrowLeft}, "Previous day", false, true, a(func(x *app.App) { x.SheetMove(0, -1) })},
		{sheet, []any{'l', gocui.KeyArrowRight}, "Next day", false, true, a(func(x *app.App) { x.SheetMove(0, 1) })},
		{sheet, []any{'k', gocui.KeyArrowUp}, "Previous task", false, true, a(func(x *app.App) { x.SheetMove(-1, 0) })},
		{sheet, []any{'j', gocui.KeyArrowDown}, "Next task", false, true, a(func(x *app.App) { x.SheetMove(1, 0) })},
		{sheet, []any{'?', 'x'}, "Keybindings", false, false, do(gui.keybindingsMenu)},
		{sheet, []any{'q'}, "Quit", false, false, func() error { return gocui.ErrQuit }},
	}
}

func (gui *Gui) layoutSheet(maxX, maxY int) error {
	bottom := maxY - 2
	for _, name := range append(append([]string{}, panels...), viewLog) {
		if v, err := gui.g.View(name); err == nil {
			v.Visible = false
		}
	}
	// The grid needs about 80 columns; below 120 the side panel makes way for it.
	sideW := min(60, maxX*38/100)
	if maxX < 120 {
		sideW = 0
	}
	v, err := gui.view(viewSheet, 0, pageTop, maxX-sideW-1, bottom)
	if err != nil {
		return err
	}
	side, err := gui.view(viewSheetSide, max(maxX-sideW, 0), pageTop, maxX-1, bottom)
	if err != nil {
		return err
	}
	side.Visible = sideW > 0
	v.HighlightInactive = gui.popup != nil
	gui.renderSheet(v)
	gui.renderSheetSide(side)
	return nil
}

func (gui *Gui) renderSheet(v *gocui.View) {
	a := gui.App
	week := a.Sheet.Week
	v.Title = "Timesheet"
	_, isoWeek := week.ISOWeek()
	v.Subtitle = fmt.Sprintf("week %d · %s – %s", isoWeek, week.Format("02 Jan"), week.AddDate(0, 0, 6).Format("02 Jan 2006"))
	rows, days, total := a.SheetRows()
	width, _ := v.InnerSize()
	header, lines, footer := render.SheetLines(rows, days, total, week, width, a.Sheet.Row, a.Sheet.Col, a.Now())
	content := []string{header}
	switch {
	case len(rows) == 0 && a.Sheet.Loading:
		content = append(content, style.Dim("loading…"))
	case len(rows) == 0:
		content = append(content, style.Dim("No time logged this week. n adds a task row; L on a task in the main view logs time."))
	default:
		if a.Sheet.Row < len(lines) {
			lines[a.Sheet.Row] = style.Strip(lines[a.Sheet.Row]) // plain text reads best on the selection bar
		}
		content = append(content, lines...)
	}
	content = append(content, "", footer)
	write(v, content)
	v.FocusPoint(0, a.Sheet.Row+1, true)
	v.Footer = "total " + parse.Hours(total)
}

func (gui *Gui) renderSheetSide(v *gocui.View) {
	a := gui.App
	rows, _, _ := a.SheetRows()
	day := a.Sheet.Week.AddDate(0, 0, a.Sheet.Col)
	v.Title = day.Format("Monday 02 January")
	var lines []string
	if a.Sheet.Row < len(rows) {
		r := rows[a.Sheet.Row]
		lines = append(lines, style.Bold(strings.TrimSpace(r.Label+" "+r.Name)))
		if r.List != "" {
			lines = append(lines, style.Dim(r.List))
		}
		lines = append(lines, "")
		entries := a.SelectedEntries()
		if len(entries) == 0 {
			lines = append(lines, style.Dim("No time on this day. enter adds some."))
		}
		for _, e := range entries {
			lines = append(lines, render.EntryLine(e, a.Now(), false))
		}
		lines = append(lines, "", style.Dim("week total for this task: ")+parse.Hours(r.Total))
	}
	if t := a.TimerLine(); t != "" {
		lines = append(lines, "", t)
	}
	lines = append(lines, "", style.Dim("enter adds an entry to this day: type the duration;"),
		style.Dim("↓ reaches the start, end and note."),
		style.Dim("e edits or deletes one of this day's entries."))
	write(v, lines)
}

// clockTick redraws once a second while a timer runs, so its clock moves.
func (gui *Gui) clockTick() {
	t := time.NewTicker(time.Second)
	for range t.C {
		if gui.App.TimerRunning() {
			gui.async.UI(func() {})
		}
	}
}

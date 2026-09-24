// Package render turns ClickUp data into styled lines for the panels.
package render

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

var statusRank = map[string]int{"open": 0, "custom": 1, "unstarted": 1, "active": 1, "done": 2, "closed": 3}

var PriorityColors = map[string]string{
	"urgent": "#f50000", "high": "#ffcc00", "normal": "#6fddff", "low": "#d8d8d8",
}

// Millis parses a ClickUp millisecond timestamp.
func Millis(ms clickup.FlexString) (time.Time, bool) {
	n := ms.Int()
	if n == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(n), true
}

func rank(s clickup.Status) int {
	if r, ok := statusRank[s.Type]; ok {
		return r
	}
	return 1
}

// compareTasks orders like ClickUp's list view. The id comes last, so tasks that tie (say,
// from different lists in Mine) keep a fixed order instead of the order they arrived in.
func compareTasks(a, b *clickup.Task) int {
	return cmp.Or(
		cmp.Compare(rank(a.Status), rank(b.Status)),
		cmp.Compare(a.Status.OrderIndex, b.Status.OrderIndex),
		cmp.Compare(a.Status.Status, b.Status.Status),
		cmp.Compare(a.OrderIndex, b.OrderIndex),
		cmp.Compare(a.ID, b.ID),
	)
}

// Row is a task in display order with its subtask depth and the group it's listed under.
type Row struct {
	Task  *clickup.Task
	Depth int
	Group Group
}

// Ordered sorts like ClickUp's list view: by status, subtasks nested under their parent.
func Ordered(tasks []*clickup.Task) []Row {
	byID := make(map[string]*clickup.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	children := map[string][]*clickup.Task{}
	var roots []*clickup.Task
	for _, t := range tasks {
		if p := string(t.Parent); p != "" && byID[p] != nil {
			children[p] = append(children[p], t)
		} else {
			roots = append(roots, t)
		}
	}
	rows := make([]Row, 0, len(tasks))
	seen := map[string]bool{}
	var visit func(*clickup.Task, int)
	visit = func(t *clickup.Task, depth int) {
		if seen[t.ID] {
			return
		}
		seen[t.ID] = true
		rows = append(rows, Row{Task: t, Depth: depth})
		kids := children[t.ID]
		slices.SortStableFunc(kids, func(a, b *clickup.Task) int { return cmp.Compare(a.OrderIndex, b.OrderIndex) })
		for _, kid := range kids {
			visit(kid, depth+1)
		}
	}
	slices.SortStableFunc(roots, compareTasks)
	for _, r := range roots {
		visit(r, 0)
	}
	return rows
}

// Matches reports whether every word of needle occurs in the task's searchable text.
func Matches(t *clickup.Task, needle string) bool {
	if needle == "" {
		return true
	}
	parts := []string{t.Name, t.ID, t.CustomID, t.Status.Status, t.List.Name}
	for _, a := range t.Assignees {
		parts = append(parts, a.Username)
	}
	for _, tag := range t.Tags {
		parts = append(parts, tag.Name)
	}
	for i := range t.CustomFields {
		if f := &t.CustomFields[i]; f.Type == "drop_down" {
			if o, ok := DropdownValue(f); ok {
				parts = append(parts, o.Title())
			}
		}
	}
	hay := strings.ToLower(strings.Join(parts, " "))
	for word := range strings.FieldsSeq(strings.ToLower(needle)) {
		if !strings.Contains(hay, word) {
			return false
		}
	}
	return true
}

func StatusCell(t *clickup.Task) style.Cell {
	return style.Cell{Text: "● " + strings.ToUpper(t.Status.Status), Style: style.Fg(t.Status.Color, style.Dim)}
}

func NameCell(row Row) (prefix, name style.Cell) {
	if row.Depth > 0 {
		prefix = style.Cell{Text: strings.Repeat("  ", row.Depth-1) + "  ↳ ", Style: style.Dim}
	}
	name = style.Cell{Text: row.Task.Name}
	if row.Task.Status.Closed() {
		name.Style = style.Dim
	}
	if row.Task.Pending {
		name.Text += " …"
	}
	return prefix, name
}

func Initials(u clickup.User) string {
	if u.Initials != "" {
		return u.Initials
	}
	r := []rune(strings.ToUpper(u.Username))
	return string(r[:min(2, len(r))])
}

// AssigneesText shows up to two people's initials; more become "+N" so the column fits.
func AssigneesText(t *clickup.Task) string {
	shown := t.Assignees
	if len(shown) > 2 {
		shown = shown[:1]
	}
	parts := make([]string, len(shown))
	for i, a := range shown {
		parts[i] = style.Fg(a.Color, style.Bold)(Initials(a))
	}
	if more := len(t.Assignees) - len(shown); more > 0 {
		parts = append(parts, style.Dim("+"+strconv.Itoa(more)))
	}
	return strings.Join(parts, " ")
}

func DueCell(t *clickup.Task, now time.Time) style.Cell {
	due, ok := Millis(t.DueDate)
	if !ok {
		return style.Cell{}
	}
	label := due.Format("Jan 02")
	if due.Year() != now.Year() {
		label = due.Format("Jan 2006")
	}
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	switch {
	case t.Status.Closed():
		return style.Cell{Text: label, Style: style.Dim}
	case day(due) < day(now):
		return style.Cell{Text: label, Style: style.BoldRed}
	case day(due) == day(now):
		return style.Cell{Text: label, Style: style.Yellow}
	}
	return style.Cell{Text: label}
}

func PriorityFlag(t *clickup.Task, label bool) string {
	if t.Priority == nil {
		return ""
	}
	text := "⚑"
	if label {
		text += " " + t.Priority.Priority
	}
	color := cmp.Or(PriorityColors[t.Priority.Priority], t.Priority.Color)
	return style.Fg(color, style.Plain)(text)
}

// Table is the column layout of the task panel for a given width.
type Table struct {
	Status, Name, Who, Due, List int
	Dropdowns                    []DropdownColumn
}

// NewTable fits columns into width; dropdown fields get columns while the name keeps ≥24 cols.
func NewTable(tasks []*clickup.Task, width int, showList bool) Table {
	t := Table{Status: 14, Who: 6, Due: 6}
	if width < 90 { // narrow: the coloured dot says enough, the name needs the room
		t.Status = 2
	}
	fixed := t.Status + t.Who + t.Due + 1 + 4 // flag column, one space before each of name, who, due, flag
	if showList && width >= 80 {
		t.List = 14
		fixed += t.List + 1
	}
	for _, col := range DropdownColumns(tasks) {
		if width-fixed-col.Width-1 < 24 {
			break
		}
		t.Dropdowns = append(t.Dropdowns, col)
		fixed += col.Width + 1
	}
	t.Name = max(12, width-fixed)
	return t
}

// Line renders one task row.
func (tbl Table) Line(row Row, now time.Time) string {
	t := row.Task
	prefix, name := NameCell(row)
	nameWidth := tbl.Name - style.Width(prefix.Text)
	var b strings.Builder
	status := StatusCell(t)
	if tbl.Status <= 2 {
		status.Text = "●"
	}
	b.WriteString(status.Fit(tbl.Status))
	b.WriteString(" ")
	b.WriteString(prefix.Fit(style.Width(prefix.Text)))
	b.WriteString(name.Fit(nameWidth))
	for _, col := range tbl.Dropdowns {
		b.WriteString(" ")
		b.WriteString(style.Raw(DropdownCell(t, col), col.Width))
	}
	b.WriteString(" ")
	b.WriteString(style.Raw(AssigneesText(t), tbl.Who))
	b.WriteString(" ")
	b.WriteString(DueCell(t, now).Fit(tbl.Due))
	b.WriteString(" ")
	b.WriteString(style.Raw(PriorityFlag(t, false), 1))
	if tbl.List > 0 {
		b.WriteString(" ")
		b.WriteString(style.Cell{Text: t.List.Name, Style: style.Dim}.Fit(tbl.List))
	}
	return b.String()
}

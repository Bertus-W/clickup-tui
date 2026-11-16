package app

import (
	"fmt"
	"strings"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

// Visual mode selects a range of tasks in the list, like vim's: v starts it at the highlighted
// task, moving extends it from there in either direction, v or esc ends it, and an action ends
// it once it worked on the range. Actions that work on one task refuse while it's on (see
// current) rather than quietly reaching only the highlighted one.

// Visual reports whether visual mode is on.
func (a *App) Visual() bool { return a.visualFrom != "" }

// ToggleVisual starts visual mode at the highlighted task, or ends it.
func (a *App) ToggleVisual() {
	if a.Visual() {
		a.EndVisual()
		return
	}
	if a.SelectedIndex() >= 0 {
		a.visualFrom = a.SelectedID
	}
}

func (a *App) EndVisual() { a.visualFrom = "" }

// VisualRange is the selected rows of Rows, lo to hi inclusive; ok is false outside visual mode.
func (a *App) VisualRange() (lo, hi int, ok bool) {
	if !a.Visual() {
		return 0, 0, false
	}
	from, to := -1, -1
	for i, r := range a.Rows() {
		if r.Task.ID == a.visualFrom {
			from = i
		}
		if r.Task.ID == a.SelectedID {
			to = i
		}
	}
	if from < 0 || to < 0 {
		return 0, 0, false
	}
	return min(from, to), max(from, to), true
}

// Selected is the tasks in the visual range, in list order; nil outside visual mode.
func (a *App) Selected() []*clickup.Task {
	lo, hi, ok := a.VisualRange()
	if !ok {
		return nil
	}
	var out []*clickup.Task
	for _, r := range a.Rows()[lo : hi+1] {
		out = append(out, r.Task)
	}
	return out
}

// keepVisual ends visual mode when the task it started at is no longer listed.
func (a *App) keepVisual() {
	if _, _, ok := a.VisualRange(); !ok {
		a.EndVisual()
	}
}

// copySelected offers the selected tasks' ids, names and links for the clipboard, a task per
// line.
func (a *App) copySelected() {
	tasks := a.Selected()
	lines := func(f func(t *clickup.Task) string) string {
		out := make([]string, len(tasks))
		for i, t := range tasks {
			out[i] = f(t)
		}
		return strings.Join(out, "\n")
	}
	items := []MenuItem{
		{'i', "IDs", lines(func(t *clickup.Task) string { return t.Label() })},
		{'u', "URLs", lines(func(t *clickup.Task) string { return t.URL })},
		{'n', "Names", lines(func(t *clickup.Task) string { return t.Name })},
		{'l', "ID and name", lines(func(t *clickup.Task) string { return t.Label() + " " + t.Name })},
		{'m', "Markdown links", lines(func(t *clickup.Task) string { return fmt.Sprintf("- [%s](%s)", t.Name, t.URL) })},
	}
	for _, t := range tasks {
		if t.CustomID != "" {
			items = append(items, MenuItem{'I', "Internal IDs", lines(func(t *clickup.Task) string { return t.ID })})
			break
		}
	}
	a.UI.Menu(fmt.Sprintf("Copy %d tasks", len(tasks)), items, 0, func(item MenuItem) {
		a.copyLines(item.Value.(string), len(tasks), item.Label)
	})
}

// copyLines copies what copySelected offers and ends visual mode.
func (a *App) copyLines(text string, n int, what string) {
	if err := a.UI.Clipboard(text); err != nil {
		a.error("Copy", err)
		return
	}
	a.EndVisual()
	a.UI.Notify(Info, fmt.Sprintf("Copied %s of %d tasks", what, n))
}

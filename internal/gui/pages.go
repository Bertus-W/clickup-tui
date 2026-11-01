package gui

import (
	"strings"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Pages are shown as tabs on the top row: H opens the timesheet, esc goes back to the tasks
// (F2/F1 work too), or click a tab.

const (
	viewPages = "pages"
	pageTop   = 1 // pages start below the tab bar
)

type page int

const (
	pageTasks page = iota
	pageTimesheet
)

var pageNames = []string{"Tasks", "Timesheet"}

var pageKeys = []string{"esc", "H"}

func (gui *Gui) page() page {
	if gui.sheetOpen {
		return pageTimesheet
	}
	return pageTasks
}

// showPage switches pages; popups keep the focus while they're open.
func (gui *Gui) showPage(p page) {
	if gui.popup != nil || p == gui.page() {
		return
	}
	gui.filtering = false
	if p == pageTimesheet {
		gui.openSheet()
	} else {
		gui.closeSheet()
	}
}

func (gui *Gui) renderPages(maxX int) error {
	v, err := gui.view(viewPages, -1, -1, maxX, 1)
	if err != nil {
		return err
	}
	gui.pageTabs = gui.pageTabs[:0]
	var b strings.Builder
	x := 1
	for i, name := range pageNames {
		label := " " + pageKeys[i] + " " + name + " "
		if page(i) == gui.page() {
			b.WriteString(style.Chip(strings.TrimSpace(label), "#5faf5f"))
		} else {
			b.WriteString(style.Dim(label))
		}
		gui.pageTabs = append(gui.pageTabs, [2]int{x, x + len([]rune(label))})
		x += len([]rune(label)) + 1
		b.WriteString(" ")
	}
	right := gui.App.TimerLine()
	if right == "" {
		right = style.Dim("H timesheet · esc back")
	}
	left := b.String()
	gap := maxX - 2 - style.Width(left) - style.Width(right)
	if gap < 1 {
		right, gap = "", 0 // no room: the tabs matter more
	}
	write(v, []string{" " + left + strings.Repeat(" ", gap) + right})
	return nil
}

func (gui *Gui) pageClick(o gocui.ViewMouseBindingOpts) error {
	for i, tab := range gui.pageTabs {
		if o.X >= tab[0] && o.X < tab[1] {
			gui.showPage(page(i))
		}
	}
	return nil
}

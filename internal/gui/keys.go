package gui

import (
	"slices"
	"strings"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// binding is one entry of the keymap. The same table drives gocui, the hint line at the
// bottom (short) and the `?` keybindings menu.
type binding struct {
	views   []string
	keys    []any // rune or gocui.Key
	desc    string
	short   bool // show in the bottom hint line
	nav     bool // listed last in the keybindings menu
	handler func() error
}

var taskViews = []string{viewTasks, viewPinned, viewDetail}

// do wraps an app action as a key handler.
func do(fn func()) func() error { return func() error { fn(); return nil } }

func (gui *Gui) keymap() []binding {
	a := func(fn func(*app.App)) func() error { return func() error { fn(gui.App); return nil } }
	return []binding{
		// The hint line shows the short ones in this order, as far as they fit.
		{taskViews, []any{gocui.KeySpace}, "Status", true, false, a((*app.App).NextStatus)},
		{[]string{viewTasks}, []any{'/'}, "Filter", true, false, do(gui.startFilter)},
		{[]string{viewTasks, viewPinned}, []any{gocui.KeyEnter}, "View", true, false, do(func() { gui.focus(viewDetail) })},
		{taskViews, []any{'f'}, "Fields", true, false, a((*app.App).EditField)},
		{taskViews, []any{'L'}, "Log time", true, false, a((*app.App).LogTime)},
		{taskViews, []any{'A'}, "Assign", true, false, a((*app.App).Assign)},
		{taskViews, []any{'n'}, "New task", true, false, a(func(x *app.App) { x.NewTask(false) })},
		{taskViews, []any{'P'}, "Pin", true, false, a((*app.App).TogglePin)},
		{taskViews, []any{'c'}, "Comment", true, false, a((*app.App).Comment)},
		{taskViews, []any{'e'}, "Edit description", true, false, a((*app.App).EditDescription)},

		// Task actions: available on the task list and the task panel.
		{taskViews, []any{'s'}, "Find status", false, false, a((*app.App).SetStatus)},
		{taskViews, []any{'T'}, "Start/stop timer", false, false, a((*app.App).ToggleTimer)},
		{taskViews, []any{'w'}, "Time entries", false, false, a((*app.App).TaskTime)},
		{taskViews, []any{'a'}, "Assign/unassign me", false, false, a((*app.App).AssignMe)},
		{taskViews, []any{'N'}, "New subtask", false, false, a(func(x *app.App) { x.NewTask(true) })},
		{taskViews, []any{'r'}, "Rename", false, false, a((*app.App).Rename)},
		{taskViews, []any{'m'}, "Move to list", false, false, a((*app.App).MoveTask)},
		{taskViews, []any{'C'}, "Comment in $EDITOR", false, false, a((*app.App).CommentInEditor)},
		{taskViews, []any{'p'}, "Priority", false, false, a((*app.App).SetPriority)},
		{taskViews, []any{'t'}, "Due date", false, false, a((*app.App).SetDue)},
		{taskViews, []any{'d'}, "Delete", false, false, a((*app.App).Delete)},
		{taskViews, []any{'o'}, "Open in browser", false, false, a((*app.App).OpenInBrowser)},
		{taskViews, []any{'y'}, "Copy…", false, false, a((*app.App).CopyMenu)},

		// Task list.
		{[]string{viewTasks}, []any{gocui.KeyEsc}, "Clear filter", false, false, do(func() { gui.App.SetFilter("") })},
		{[]string{viewTasks}, []any{'v'}, "Show closed", false, false, a((*app.App).ToggleClosed)},
		{[]string{viewTasks}, []any{'S'}, "Sort by…", false, false, a((*app.App).SortMenu)},
		{[]string{viewTasks}, []any{'[', ']'}, "Switch tab (list ↔ mine)", false, false, a((*app.App).SwitchTab)},
		{[]string{viewTasks}, []any{'j', gocui.KeyArrowDown}, "Down", false, true, do(func() { gui.moveTask(1) })},
		{[]string{viewTasks}, []any{'k', gocui.KeyArrowUp}, "Up", false, true, do(func() { gui.moveTask(-1) })},
		{[]string{viewTasks}, []any{',', gocui.KeyPgup}, "Previous page", false, true, do(func() { gui.moveTask(-gui.pageSize(viewTasks)) })},
		{[]string{viewTasks}, []any{'.', gocui.KeyPgdn}, "Next page", false, true, do(func() { gui.moveTask(gui.pageSize(viewTasks)) })},
		{[]string{viewTasks}, []any{'<', gocui.KeyHome}, "Top", false, true, do(func() { gui.App.Select(0) })},
		{[]string{viewTasks}, []any{'>', gocui.KeyEnd}, "Bottom", false, true, do(func() { gui.App.Select(len(gui.App.Rows()) - 1) })},

		// Task panel.
		{[]string{viewDetail}, []any{'j', gocui.KeyArrowDown}, "Scroll down", false, true, do(func() { gui.scrollDetail(1) })},
		{[]string{viewDetail}, []any{'k', gocui.KeyArrowUp}, "Scroll up", false, true, do(func() { gui.scrollDetail(-1) })},
		{[]string{viewDetail}, []any{gocui.KeyEsc}, "Back to the list", false, true, do(func() { gui.focus(gui.lastList) })},

		// Pinned.
		{[]string{viewPinned}, []any{'j', gocui.KeyArrowDown}, "Down", false, true, do(func() { gui.App.SelectPinned(gui.App.PinnedSel + 1) })},
		{[]string{viewPinned}, []any{'k', gocui.KeyArrowUp}, "Up", false, true, do(func() { gui.App.SelectPinned(gui.App.PinnedSel - 1) })},

		// Lists.
		{[]string{viewLists}, []any{gocui.KeyEnter}, "Open", true, false, do(gui.openTreeLine)},
		{[]string{viewLists}, []any{gocui.KeySpace}, "Expand/collapse", false, false, do(gui.toggleTreeLine)},
		{[]string{viewLists}, []any{'/'}, "Search lists", true, false, a((*app.App).JumpToList)},
		{[]string{viewLists}, []any{'j', gocui.KeyArrowDown}, "Down", false, true, do(func() { gui.treeSel++ })},
		{[]string{viewLists}, []any{'k', gocui.KeyArrowUp}, "Up", false, true, do(func() { gui.treeSel = max(gui.treeSel-1, 0) })},

		// Workspace.
		{[]string{viewStatus}, []any{gocui.KeyEnter}, "Switch workspace", true, false, a((*app.App).SwitchWorkspace)},

		// Pages. The tab bar shows the keys, so they stay out of the hint line. Back to the
		// tasks is esc on the timesheet (sheetKeys), so ? doesn't offer the page you're on.
		{panels, []any{'H', gocui.KeyF2}, "Timesheet page (hours)", false, false, do(func() { gui.showPage(pageTimesheet) })},

		// Global.
		{panels, []any{'g'}, "Go to task by id/URL", false, false, a((*app.App).GoTo)},
		{panels, []any{gocui.KeyCtrlP}, "Jump to list", false, false, a((*app.App).JumpToList)},
		{panels, []any{'R'}, "Refresh", false, false, a((*app.App).Refresh)},
		{panels, []any{'+'}, "Next screen mode", false, false, do(func() { gui.mode = (gui.mode + 1) % 3 })},
		{panels, []any{'_'}, "Previous screen mode", false, false, do(func() { gui.mode = (gui.mode + 2) % 3 })},
		{panels, []any{'@'}, "Toggle command log", false, false, do(func() { gui.showLog = !gui.showLog })},
		{append(slices.Clone(panels), viewSheet), []any{'M'}, "Mouse on/off (off: select text to copy)", false, false, do(gui.toggleMouse)},
		{panels, []any{'J', gocui.KeyCtrlD}, "Scroll task down", false, true, do(func() { gui.scrollDetail(gui.pageSize(viewDetail) / 2) })},
		{panels, []any{'K', gocui.KeyCtrlU}, "Scroll task up", false, true, do(func() { gui.scrollDetail(-gui.pageSize(viewDetail) / 2) })},
		{panels, []any{'0'}, "Focus task panel", false, true, do(func() { gui.focus(viewDetail) })},
		{panels, []any{'1'}, "Focus workspace", false, true, do(func() { gui.focus(viewStatus) })},
		{panels, []any{'2'}, "Focus lists", false, true, do(func() { gui.focus(viewLists) })},
		{panels, []any{'3'}, "Focus tasks", false, true, do(func() { gui.focus(viewTasks) })},
		{panels, []any{'4'}, "Focus pinned", false, true, do(func() { gui.focus(viewPinned) })},
		{panels, []any{'l', gocui.KeyArrowRight, gocui.KeyTab}, "Next panel", false, true, do(func() { gui.cyclePanel(1) })},
		{panels, []any{'h', gocui.KeyArrowLeft, gocui.KeyBacktab}, "Previous panel", false, true, do(func() { gui.cyclePanel(-1) })},
		{panels, []any{'?', 'x'}, "Keybindings", false, false, do(gui.keybindingsMenu)},
		{panels, []any{'q'}, "Quit", false, false, func() error { return gocui.ErrQuit }},
	}
}

// register installs the keymap plus popup and filter keys in gocui.
func (gui *Gui) register() error {
	set := func(view string, key any, h func() error) error {
		return gui.g.SetKeybinding(view, key, gocui.ModNone, func(*gocui.Gui, *gocui.View) error { return h() })
	}
	for _, b := range gui.bindings {
		for _, view := range b.views {
			for _, key := range b.keys {
				if err := set(view, key, b.handler); err != nil {
					return err
				}
			}
		}
	}
	for key, h := range gui.fixedKeys() {
		if err := set(key.view, key.key, h); err != nil {
			return err
		}
	}
	return set("", gocui.KeyCtrlC, func() error { return gocui.ErrQuit })
}

// formSubmitKey is ctrl+s: create from anywhere in the new task form.
func (gui *Gui) formSubmitKey() error {
	if gui.popup == nil || gui.popup.kind != popupForm {
		return nil
	}
	defer gui.resumeForm()
	return gui.formSubmit()
}

type viewKey struct {
	view string
	key  any
}

// fixedKeys are the keys of popups and the filter line; they aren't listed in the menus.
func (gui *Gui) fixedKeys() map[viewKey]func() error {
	keys := map[viewKey]func() error{
		{viewPopup, gocui.KeyEnter}:          gui.popupConfirm,
		{viewPopup, gocui.KeyEsc}:            gui.popupCancel,
		{viewPopup, 'q'}:                     gui.popupCancel,
		{viewPopup, gocui.KeySpace}:          func() error { return gui.popupRune(' ') },
		{viewPopup, gocui.KeyArrowDown}:      do(func() { gui.popupMove(1) }),
		{viewPopup, gocui.KeyArrowUp}:        do(func() { gui.popupMove(-1) }),
		{viewPopup, gocui.KeyTab}:            do(gui.popupTab),
		{viewPopup, gocui.KeyCtrlS}:          gui.formSubmitKey,
		{viewPopupInput, gocui.KeyEnter}:     gui.popupConfirm,
		{viewPopupInput, gocui.KeyEsc}:       gui.popupCancel,
		{viewPopupInput, gocui.KeyArrowDown}: do(func() { gui.popupMove(1) }),
		{viewPopupInput, gocui.KeyArrowUp}:   do(func() { gui.popupMove(-1) }),
		{viewPopupInput, gocui.KeyCtrlJ}:     do(func() { gui.popupMove(1) }),
		{viewPopupInput, gocui.KeyCtrlK}:     do(func() { gui.popupMove(-1) }),
		{viewPopupInput, gocui.KeyTab}:       do(gui.popupTab),
		{viewPopupInput, gocui.KeyCtrlS}:     gui.formSubmitKey,
		{viewFilter, gocui.KeyEnter}:         do(gui.endFilter),
		{viewFilter, gocui.KeyEsc}:           do(func() { gui.App.SetFilter(""); gui.endFilter() }),
		{viewFilter, gocui.KeyArrowDown}:     do(func() { gui.moveTask(1) }),
		{viewFilter, gocui.KeyArrowUp}:       do(func() { gui.moveTask(-1) }),
	}
	for r := rune('!'); r <= '~'; r++ {
		if r != 'q' {
			keys[viewKey{viewPopup, r}] = func() error { return gui.popupRune(r) }
		}
	}
	return keys
}

func keyName(key any) string {
	switch k := key.(type) {
	case rune:
		if k == ' ' {
			return "space"
		}
		return string(k)
	case gocui.Key:
		if name, ok := map[gocui.Key]string{
			gocui.KeySpace: "space", gocui.KeyEnter: "enter", gocui.KeyEsc: "esc", gocui.KeyTab: "tab",
			gocui.KeyBacktab: "shift+tab", gocui.KeyArrowUp: "↑", gocui.KeyArrowDown: "↓",
			gocui.KeyArrowLeft: "←", gocui.KeyArrowRight: "→", gocui.KeyPgup: "pgup", gocui.KeyPgdn: "pgdn",
			gocui.KeyHome: "home", gocui.KeyEnd: "end", gocui.KeyCtrlP: "ctrl+p", gocui.KeyCtrlD: "ctrl+d",
			gocui.KeyCtrlU: "ctrl+u", gocui.KeyCtrlC: "ctrl+c", gocui.KeyCtrlJ: "ctrl+j", gocui.KeyCtrlK: "ctrl+k",
		}[k]; ok {
			return name
		}
	}
	return "?"
}

// active lists the bindings of the focused panel: panel-specific first, navigation last.
func (gui *Gui) active() []binding {
	var own, global []binding
	for _, b := range gui.bindings {
		switch {
		case !slices.Contains(b.views, gui.panel):
		case len(b.views) == len(panels):
			global = append(global, b)
		default:
			own = append(own, b)
		}
	}
	all := append(own, global...)
	slices.SortStableFunc(all, func(x, y binding) int {
		if x.nav == y.nav {
			return 0
		}
		if x.nav {
			return 1
		}
		return -1
	})
	return all
}

// hints renders lazygit's bottom line, "Done: space | Status: s | … | Keybindings: ?",
// dropping hints from the end to fit width while always keeping the keybindings hint.
func (gui *Gui) hints(width int) string {
	var parts []string
	for _, b := range gui.bindings { // in keymap order: the keymap decides what comes first
		if b.short && slices.Contains(b.views, gui.panel) {
			parts = append(parts, b.desc+": "+keyName(b.keys[0]))
		}
	}
	line := func() string { return strings.Join(append(slices.Clone(parts), "Keybindings: ?"), " | ") }
	for len(parts) > 0 && style.Width(line()) > width {
		parts = parts[:len(parts)-1]
	}
	return style.Cyan(line())
}

// keybindingsMenu lists every binding of the focused panel; pressing a key in it runs the action.
func (gui *Gui) keybindingsMenu() {
	var items []app.MenuItem
	var labels []string
	for _, b := range gui.active() {
		key, _ := b.keys[0].(rune)
		if key == 'j' || key == 'k' {
			key = 0 // j/k move the cursor in menus
		}
		items = append(items, app.MenuItem{Key: key, Label: b.desc, Value: b.handler})
		labels = append(labels, keyName(b.keys[0]))
	}
	gui.open(&popup{kind: popupMenu, title: "Keybindings", items: items, keyLabels: labels,
		onMenu: func(item app.MenuItem) { gui.menuErr = item.Value.(func() error)() }})
}

// --- panel handlers -------------------------------------------------------------------------------

func (gui *Gui) pageSize(view string) int {
	v, err := gui.g.View(view)
	if err != nil {
		return 10
	}
	_, h := v.InnerSize()
	return max(h, 2)
}

func (gui *Gui) moveTask(delta int) {
	gui.App.Select(max(gui.App.SelectedIndex(), 0) + delta)
}

func (gui *Gui) scrollDetail(delta int) { gui.scroll(viewDetail, delta) }

func (gui *Gui) toggleTreeLine() {
	if gui.treeSel < len(gui.tree) {
		if l := gui.tree[gui.treeSel]; l.branch {
			gui.expanded[l.key] = !gui.expanded[l.key]
		}
	}
}

func (gui *Gui) openTreeLine() {
	if gui.treeSel >= len(gui.tree) {
		return
	}
	l := gui.tree[gui.treeSel]
	if l.branch {
		gui.toggleTreeLine()
		return
	}
	gui.App.OpenView(*l.view)
	gui.focus(viewTasks)
}

func (gui *Gui) startFilter() {
	gui.filtering = true
	gui.filterFilled = false
}

func (gui *Gui) endFilter() {
	gui.filtering = false
}

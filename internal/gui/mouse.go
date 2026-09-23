package gui

import (
	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/render"
)

type mouseHandler = func(gocui.ViewMouseBindingOpts) error

// mouseMap is the mouse equivalent of the keymap: click to focus and select, double-click
// to open, wheel to move or scroll, click a popup option to pick it.
func (gui *Gui) mouseMap() map[viewKey]mouseHandler {
	return map[viewKey]mouseHandler{
		{viewStatus, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			gui.focus(viewStatus)
			if o.IsDoubleClick {
				gui.App.SwitchWorkspace()
			}
			return nil
		},
		{viewLists, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			gui.focus(viewLists)
			if o.Y >= 0 && o.Y < len(gui.tree) { // Y is -1 on the title border
				gui.treeSel = o.Y
				if o.IsDoubleClick || gui.tree[o.Y].branch {
					gui.openTreeLine() // a branch toggles on a single click, like a tree should
				}
			}
			return nil
		},
		{viewTasks, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			gui.focus(viewTasks)
			if o.Y >= 0 && o.Y < len(gui.taskLines) && gui.taskLines[o.Y] >= 0 { // not on a heading
				gui.App.Select(gui.taskLines[o.Y])
				if o.IsDoubleClick {
					gui.focus(viewDetail)
				}
			}
			return nil
		},
		{viewPinned, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			gui.focus(viewPinned)
			if o.Y >= 0 && o.Y < len(gui.App.Pinned) {
				gui.App.SelectPinned(o.Y)
				if o.IsDoubleClick {
					gui.focus(viewDetail)
				}
			}
			return nil
		},
		{viewPinned, gocui.MouseWheelDown}: func(gocui.ViewMouseBindingOpts) error {
			gui.App.SelectPinned(gui.App.PinnedSel + 1)
			return nil
		},
		{viewPinned, gocui.MouseWheelUp}: func(gocui.ViewMouseBindingOpts) error {
			gui.App.SelectPinned(gui.App.PinnedSel - 1)
			return nil
		},
		{viewDetail, gocui.MouseLeft}: func(gocui.ViewMouseBindingOpts) error { gui.focus(viewDetail); return nil },
		{viewSheet, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			rows, _, _ := gui.App.SheetRows()
			v, _ := gui.g.View(viewSheet)
			width, _ := v.InnerSize()
			if row := o.Y - 1; row >= 0 && row < len(rows) { // line 0 is the header
				gui.App.Sheet.Row = row
				if col := render.SheetColumnAt(width, o.X); col >= 0 {
					gui.App.Sheet.Col = col
					if o.IsDoubleClick {
						gui.App.AddToCell()
					}
				}
			}
			return nil
		},
		{viewSheet, gocui.MouseWheelDown}: func(gocui.ViewMouseBindingOpts) error { gui.App.SheetMove(1, 0); return nil },
		{viewSheet, gocui.MouseWheelUp}:   func(gocui.ViewMouseBindingOpts) error { gui.App.SheetMove(-1, 0); return nil },

		{viewLists, gocui.MouseWheelDown}:  func(gocui.ViewMouseBindingOpts) error { gui.treeSel++; return nil },
		{viewLists, gocui.MouseWheelUp}:    func(gocui.ViewMouseBindingOpts) error { gui.treeSel = max(gui.treeSel-1, 0); return nil },
		{viewTasks, gocui.MouseWheelDown}:  func(gocui.ViewMouseBindingOpts) error { gui.moveTask(1); return nil },
		{viewTasks, gocui.MouseWheelUp}:    func(gocui.ViewMouseBindingOpts) error { gui.moveTask(-1); return nil },
		{viewDetail, gocui.MouseWheelDown}: func(gocui.ViewMouseBindingOpts) error { gui.scrollDetail(3); return nil },
		{viewDetail, gocui.MouseWheelUp}:   func(gocui.ViewMouseBindingOpts) error { gui.scrollDetail(-3); return nil },
		{viewLog, gocui.MouseWheelDown}:    func(gocui.ViewMouseBindingOpts) error { gui.scroll(viewLog, 3); return nil },
		{viewLog, gocui.MouseWheelUp}:      func(gocui.ViewMouseBindingOpts) error { gui.scroll(viewLog, -3); return nil },

		{viewPopup, gocui.MouseLeft}: func(o gocui.ViewMouseBindingOpts) error {
			p := gui.popup
			if p == nil || p.kind == popupConfirm || p.kind == popupInfo || o.Y < 0 || o.Y >= p.count() {
				return nil
			}
			p.sel = o.Y
			if p.kind == popupForm { // clicking a property edits it
				p.inList = true
				gui.formEdit(o.Y)
				return nil
			}
			if p.kind == popupMulti {
				return gui.popupRune(' ') // toggle; enter still finishes
			}
			return gui.popupConfirm()
		},
		{viewPopup, gocui.MouseWheelDown}: func(gocui.ViewMouseBindingOpts) error { gui.popup.move(1); return nil },
		{viewPopup, gocui.MouseWheelUp}:   func(gocui.ViewMouseBindingOpts) error { gui.popup.move(-1); return nil },
		{viewPages, gocui.MouseLeft}:      gui.pageClick,
	}
}

func (gui *Gui) registerMouse() error {
	gui.g.Mouse = true
	// While a popup is open, only the popup reacts to the mouse.
	gui.g.ShouldHandleMouseEvent = func(v *gocui.View, _ gocui.Key) bool {
		return gui.popup == nil || v.Name() == viewPopup || v.Name() == viewPopupInput
	}
	for key, h := range gui.mouseMap() {
		err := gui.g.SetViewClickBinding(&gocui.ViewMouseBinding{ViewName: key.view, Key: key.key.(gocui.Key), Handler: h})
		if err != nil {
			return err
		}
	}
	// Clicking the "List" or "Mine" tab in the tasks title switches to it.
	return gui.g.SetTabClickBinding(viewTasks, func(tab int) error {
		if (tab == 0) != (gui.App.View.Kind == "list") {
			gui.App.SwitchTab()
		}
		gui.focus(viewTasks)
		return nil
	})
}

func (gui *Gui) scroll(view string, delta int) {
	v, err := gui.g.View(view)
	if err != nil {
		return
	}
	v.Autoscroll = false
	if delta > 0 {
		v.ScrollDown(delta)
	} else {
		v.ScrollUp(-delta)
	}
}

package gui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// testAsync runs background work in goroutines and UI callbacks under a mutex, standing
// in for the gocui event loop.
type testAsync struct {
	ui      sync.Mutex
	wg      sync.WaitGroup
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (t *testAsync) Go(group string, fn func(context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	if group != "" {
		t.mu.Lock()
		if prev := t.cancels[group]; prev != nil {
			prev()
		}
		t.cancels[group] = cancel
		t.mu.Unlock()
	}
	t.wg.Go(func() {
		defer cancel()
		fn(ctx)
	})
}

func (t *testAsync) UI(fn func()) {
	t.ui.Lock()
	defer t.ui.Unlock()
	fn()
}

type harness struct {
	t      *testing.T
	gui    *Gui
	app    *app.App
	fake   *fake.Server
	async  *testAsync
	keys   map[viewKey]func() error
	edited string // what the fake $EDITOR "saves"
}

func newHarness(t *testing.T, srv *fake.Server) *harness {
	t.Helper()
	async := &testAsync{cancels: map[string]context.CancelFunc{}}
	gui, err := New(Options{Headless: true, Width: 160, Height: 40, Async: async})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gui.g.Close)
	c, err := cache.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	a := app.New(srv.Client(), c, gui, async)
	a.Debounce = 0
	gui.Attach(a)
	h := &harness{t: t, gui: gui, app: a, fake: srv, async: async, keys: gui.fixedKeys()}
	for _, b := range gui.bindings {
		for _, view := range b.views {
			for _, key := range b.keys {
				h.keys[viewKey{view, key}] = b.handler
			}
		}
	}
	gui.editor = func(string) (string, bool, error) { return h.edited, true, nil }
	h.event(a.Boot)
	return h
}

// event runs fn on the "UI goroutine", waits for background work and redraws.
func (h *harness) event(fn func()) {
	h.t.Helper()
	h.async.UI(fn)
	h.async.wg.Wait()
	h.async.UI(func() {
		if err := h.gui.g.ForceLayoutAndRedraw(); err != nil {
			h.t.Fatal(err)
		}
	})
}

// press sends keys the way gocui dispatches them: editable views take printable
// characters, everything else goes through the keybindings of the current view.
func (h *harness) press(keys ...any) {
	h.t.Helper()
	for _, key := range keys {
		h.event(func() {
			v := h.gui.g.CurrentView()
			if r, ok := key.(rune); ok && v.Editable {
				k := gocui.Key(0)
				if r == ' ' {
					k = gocui.KeySpace
				}
				v.Editor.Edit(v, k, r, gocui.ModNone)
				return
			}
			if key == ' ' {
				key = gocui.KeySpace
			}
			handler, ok := h.keys[viewKey{v.Name(), key}]
			if !ok && v.Editable { // unbound keys go to the view's editor, as in gocui
				v.Editor.Edit(v, key.(gocui.Key), 0, gocui.ModNone)
				return
			}
			if !ok {
				h.t.Fatalf("no binding for %v in view %q", keyName(key), v.Name())
			}
			if err := handler(); err != nil && !errors.Is(err, gocui.ErrQuit) {
				h.t.Fatal(err)
			}
		})
	}
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.press(r)
	}
}

func (h *harness) screen() string { return h.gui.g.Snapshot() }

func (h *harness) current() string { return h.gui.g.CurrentView().Name() }

func (h *harness) wantScreen(parts ...string) {
	h.t.Helper()
	screen := h.screen()
	for _, part := range parts {
		if !strings.Contains(screen, part) {
			h.t.Fatalf("screen lacks %q:\n%s", part, screen)
		}
	}
}

var enter, esc = gocui.KeyEnter, gocui.KeyEsc

func TestLazygitLayout(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.wantScreen(
		"─[1]─Workspace─", "Acme → tester",
		"─[2]─Lists─", "★ My tasks", "▶ Engineering",
		"─[3]─List - Mine─", "Task number 1", "1 of 3",
		"─[0]─Task─", "Description of task 1", "first!",
		"─[4]─Pinned─", "P pins the selected task here",
		"Command log", "GET",
		"Status: space | Filter: / | View: enter | Fields: f", "Keybindings: ?",
	)
	if h.current() != viewTasks {
		t.Fatalf("focus = %q", h.current())
	}
}

func TestPanelNavigation(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('2')
	if h.current() != viewLists {
		t.Fatalf("2 → %q", h.current())
	}
	h.wantScreen("Open: enter | Search lists: /")
	h.press('l')
	if h.current() != viewTasks {
		t.Fatalf("l → %q", h.current())
	}
	h.press(enter)
	if h.current() != viewDetail {
		t.Fatalf("enter → %q", h.current())
	}
	h.press(esc)
	if h.current() != viewTasks {
		t.Fatalf("esc → %q", h.current())
	}
	h.press(gocui.KeyTab, gocui.KeyTab, gocui.KeyTab) // tasks → pinned → task panel → workspace
	if h.current() != viewStatus {
		t.Fatalf("tab ×3 → %q", h.current())
	}
}

func TestOpenListFromTreeAndSwitchTabs(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('2', 'j', ' ') // expand Engineering
	h.wantScreen("▼ Engineering", "▶ Product", "Inbox")
	h.press('j', enter, 'j', enter) // expand Product, open Backlog
	if h.app.View.ID != fake.ListID || h.current() != viewTasks {
		t.Fatalf("view = %+v, focus = %q", h.app.View, h.current())
	}
	h.wantScreen("─[3]─Backlog - Mine─", "1 of 5")
	h.press(']')
	if h.app.View.Kind != "my" {
		t.Fatalf("] → %+v", h.app.View)
	}
}

func TestFilterInBottomLine(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('/')
	if h.current() != viewFilter {
		t.Fatalf("focus = %q", h.current())
	}
	h.typeText("number 3") // n, m, b, r, 3 are all bound elsewhere; here they are text
	if rows := h.app.Rows(); len(rows) != 1 || rows[0].Task.ID != "t3" {
		t.Fatalf("rows = %v", rows)
	}
	h.press(enter)
	h.wantScreen("filter: number 3")
	h.press(esc)
	if n := len(h.app.Rows()); n != 3 {
		t.Fatalf("filter not cleared: %d rows", n)
	}
}

func TestStatusPickerTypeToFilter(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('s')
	h.wantScreen("Status", "TO DO", "IN PROGRESS", "COMPLETE")
	h.typeText("prog")
	h.press(enter)
	if got := h.fake.Task("t1").Status.Status; got != "in progress" {
		t.Fatalf("status = %q", got)
	}
	h.wantScreen("● IN PROGRESS")
}

func TestFieldHotkeys(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('f')
	h.wantScreen("Custom fields", "Severity", "Score", "(read-only)")
	h.press('v') // Severity
	h.wantScreen(" S1 ", " S2 ", "clear")
	h.press('2')
	if got := string(h.fake.Field("t1", "Severity").Value); got != "1" {
		t.Fatalf("severity = %s", got)
	}
	h.wantScreen("Severity          S2 ")
}

func TestLabelsMultiSelect(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('f', 'a') // Areas
	h.wantScreen("[ ]  frontend", "[ ]  backend")
	h.press(' ', 'j', ' ', enter)
	if got := string(h.fake.Field("t1", "Areas").Value); got != `["l1","l2"]` {
		t.Fatalf("areas = %s", got)
	}
}

// A picks all assignees at once: filter with /, space toggles, enter saves in one update.
func TestAssignPicksSeveral(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('A')
	h.wantScreen("Assignees · DEV-1", "[x] tester", "[ ] alice")
	h.press('/')
	h.typeText("ali")
	h.press(enter, ' ', enter)
	if a := h.fake.Task("t1").Assignees; len(a) != 2 || a[1].Username != "alice" {
		t.Fatalf("assignees = %+v", a)
	}
	if n := h.fake.Count("PUT /api/v2/task/t1"); n != 1 {
		t.Fatalf("%d updates, want 1", n)
	}
}

func TestKeybindingsMenuRunsActions(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('?')
	h.wantScreen("Keybindings", "space   Status", "Log time", "Assign/unassign me", "Timesheet page")
	if strings.Contains(h.screen(), "Tasks page") {
		t.Error("the menu offers the page you're on")
	}
	h.press('s')
	h.wantScreen("Status", "IN PROGRESS") // the status picker opened
	h.press(esc)
	if h.gui.popup != nil {
		t.Fatal("popup still open")
	}
}

func TestScreenModes(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('+', '+')
	visible := func(name string) bool { v, _ := h.gui.g.View(name); return v.Visible }
	if !visible(viewTasks) || visible(viewDetail) || visible(viewLists) {
		t.Fatal("full mode should only show the tasks panel")
	}
	h.press('0')
	if !visible(viewDetail) || visible(viewTasks) {
		t.Fatal("focusing the task panel in full mode should show only it")
	}
	h.press('+')
	if !visible(viewTasks) || !visible(viewDetail) || !visible(viewLists) {
		t.Fatal("normal mode should show everything")
	}
}

func TestPromptEditorAndConfirm(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.edited = "Rewritten in **vim**"
	h.press('e')
	if got := h.fake.Task("t1").MarkdownDescription; got != h.edited {
		t.Fatalf("description = %q", got)
	}
	h.wantScreen("Rewritten in vim")

	h.press('r')
	h.wantScreen("Rename task")
	h.press(gocui.KeyCtrlU) // clear the prefilled name (bound nowhere, handled by the editor)
	h.typeText("Short")
	h.press(enter)
	if got := h.fake.Task("t1").Name; got != "Short" {
		t.Fatalf("name = %q", got)
	}

	h.press('d')
	h.wantScreen("Delete task", "Delete DEV-1 “Short”?", "enter: confirm")
	h.press(enter)
	if _, ok := h.fake.Tasks["t1"]; ok {
		t.Fatal("task not deleted")
	}
	h.wantScreen("1 of 2")
}

// The demo data renders with ClickUp's colours: chips carry 24-bit backgrounds.
func TestDemoChipsAreColoured(t *testing.T) {
	h := newHarness(t, fake.Demo())
	h.press(']') // no list yet: moves focus to the lists panel
	h.press('/')
	h.typeText("backlog")
	h.press(enter)
	h.wantScreen("Critical", "Frontend", "Backend")
	var chip string
	for _, r := range h.app.Rows() {
		if r.Task.Name == "Login redirect loop on Safari 18" {
			chip = render.DropdownCell(r.Task, render.NewTable(h.app.Tasks, 90, false).Dropdowns[1])
		}
	}
	if style.Strip(chip) != " Critical " || !strings.Contains(chip, "48;2;229;0;0") {
		t.Fatalf("severity chip = %q", chip)
	}
}

// The focused panel gets the blue selection bar, the others a grey one.
func TestSelectionBarFollowsFocus(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	inactive := func(name string) bool { v, _ := h.gui.g.View(name); return v.HighlightInactive }
	if inactive(viewTasks) || !inactive(viewLists) {
		t.Fatal("tasks focused: tasks should use the active bar, lists the inactive one")
	}
	h.press('2')
	if !inactive(viewTasks) || inactive(viewLists) {
		t.Fatal("lists focused: the bars should swap")
	}
	h.press('3', 's')
	if !inactive(viewTasks) {
		t.Fatal("with a popup open the panel behind it is inactive")
	}
}

func (h *harness) click(view string, y int, double bool) {
	h.t.Helper()
	h.mouse(view, gocui.MouseLeft, y, double)
}

func (h *harness) mouse(view string, key gocui.Key, y int, double bool) {
	h.t.Helper()
	handler, ok := h.gui.mouseMap()[viewKey{view, key}]
	if !ok {
		h.t.Fatalf("no mouse binding for %v on %q", key, view)
	}
	h.event(func() {
		if !h.gui.g.ShouldHandleMouseEvent(must(h.gui.g.View(view)), key) {
			return
		}
		if err := handler(gocui.ViewMouseBindingOpts{Y: y, Key: key, IsDoubleClick: double}); err != nil {
			h.t.Fatal(err)
		}
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestMouse(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.click(viewTasks, 3, false) // line 0 is the status heading
	if h.app.SelectedID != "t5" || h.current() != viewTasks {
		t.Fatalf("click row 3: selected %q, focus %q", h.app.SelectedID, h.current())
	}
	h.click(viewTasks, 0, false)
	if h.app.SelectedID != "t5" {
		t.Fatalf("a click on the heading selected %q", h.app.SelectedID)
	}
	h.mouse(viewTasks, gocui.MouseWheelUp, 0, false)
	if h.app.SelectedID != "t3" {
		t.Fatalf("wheel up: selected %q", h.app.SelectedID)
	}
	h.click(viewTasks, 2, true)
	if h.current() != viewDetail {
		t.Fatalf("double-click should open the task panel, focus %q", h.current())
	}

	h.click(viewLists, 1, false) // a branch opens on a single click
	h.wantScreen("▼ Engineering")
	if h.current() != viewLists {
		t.Fatalf("focus %q", h.current())
	}

	// Popups take clicks; the panels behind them don't.
	h.click(viewTasks, 1, false)
	h.press('s')
	h.click(viewTasks, 3, false)
	if h.gui.popup == nil || h.app.SelectedID != "t1" {
		t.Fatal("a click outside the popup went through")
	}
	h.click(viewPopup, 1, false) // IN PROGRESS
	if got := h.fake.Task("t1").Status.Status; got != "in progress" || h.gui.popup != nil {
		t.Fatalf("clicked option: status %q", got)
	}
}

func TestSpaceOpensStatusMenuOnNextStatus(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press(' ')
	h.wantScreen("Status", "1  ● TO DO  current", "2  ● IN PROGRESS", "3  ● COMPLETE")
	if h.gui.popup.sel != 1 {
		t.Fatalf("preselected %d, want the next status", h.gui.popup.sel)
	}
	h.press(enter)
	if got := h.fake.Task("t1").Status.Status; got != "in progress" {
		t.Fatalf("status %q", got)
	}
	h.press(' ', '3')
	if got := h.fake.Task("t1").Status.Status; got != "complete" {
		t.Fatalf("status %q", got)
	}
}

// f on a task whose list has no custom fields says so in a popup instead of silently doing nothing.
func TestFieldsOnTaskWithoutFieldsExplains(t *testing.T) {
	srv := fake.Basic(5)
	srv.Tasks["t1"].CustomFields = nil
	h := newHarness(t, srv)
	h.press('f')
	h.wantScreen("Heads up", "“Backlog” has no custom fields", "enter/esc: close")
	h.press(esc)
	if h.gui.popup != nil {
		t.Fatal("popup still open")
	}
	h.press('j', 'f')
	h.wantScreen("Custom fields", "Severity")
}

// n opens the create form; enter walks through the required Severity and creates.
func TestNewTaskForm(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.event(func() { h.app.OpenView(app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}) })
	h.press('n')
	h.wantScreen("New task in Backlog", "enter: create · ↓: fields", "* Severity", "required", "Priority")
	h.typeText("Hotfix")
	h.press(gocui.KeyTab, 'j', 'j', enter) // Priority
	h.wantScreen("Priority", "⚑ urgent")
	h.press('1')
	h.wantScreen("New task in Backlog", "⚑ urgent") // back in the form, name kept
	h.press(gocui.KeyCtrlS)                         // create: Severity is still missing
	h.wantScreen("S1", "S2", "clear")
	h.press('2')
	if h.gui.popup != nil {
		t.Fatalf("form still open:\n%s", h.screen())
	}
	created := h.app.Detail
	if created.Name != "Hotfix" || string(h.fake.Field(created.ID, "Severity").Value) != "1" {
		t.Fatalf("created %+v", created)
	}
	if p := h.fake.Task(created.ID).Priority; p == nil || p.Priority != "urgent" {
		t.Fatalf("priority = %+v", p)
	}
	h.wantScreen("Hotfix")
}

// ↑ and ↓ walk the form as one column: name, then the fields, without wrapping around.
func TestFormArrowsMoveBetweenNameAndFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.event(func() { h.app.OpenView(app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}) })
	h.press('n')
	h.typeText("Hotfix")
	h.press(gocui.KeyArrowUp) // nothing above the name
	if p := h.gui.popup; p.inList {
		t.Fatal("↑ on the name should stay on the name")
	}
	h.press(gocui.KeyArrowDown)
	if p := h.gui.popup; !p.inList || p.sel != 0 {
		t.Fatalf("↓ from the name: inList %v sel %d", p.inList, p.sel)
	}
	n := h.gui.popup.count()
	for range n + 2 {
		h.press(gocui.KeyArrowDown)
	}
	if p := h.gui.popup; p.sel != n-1 {
		t.Fatalf("↓ past the last field wrapped to %d", p.sel)
	}
	for range n {
		h.press(gocui.KeyArrowUp)
	}
	if p := h.gui.popup; p.inList {
		t.Fatal("↑ on the first field should return to the name")
	}
	h.typeText(" now")
	h.wantScreen("Hotfix now")
}

// Cancelling a property editor returns to the form with the name intact.
func TestNewTaskFormCancelKeepsForm(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.event(func() { h.app.OpenView(app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}) })
	h.press('n')
	h.typeText("Draft")
	h.press(enter) // Severity menu
	h.press(esc)   // cancel it
	h.wantScreen("New task in Backlog", "Draft")
	h.press(esc) // cancel the form
	if h.gui.popup != nil || len(h.fake.Tasks) != 5 {
		t.Fatal("form should be gone without creating anything")
	}
}

// P pins tasks from any list into [4] Pinned; the task panel follows the pinned selection.
func TestPinnedPanel(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('P', 'j', 'P') // pin t1 and t3 from Mine
	h.wantScreen("─[4]─Pinned─", "Task number 1", "Task number 3", "1 of 2")
	h.press('4')
	if h.current() != viewPinned || h.app.Detail.ID != "t1" {
		t.Fatalf("focus %q, detail %q", h.current(), h.app.Detail.ID)
	}
	h.press('j')
	if h.app.Detail.ID != "t3" {
		t.Fatalf("detail follows pinned selection: %q", h.app.Detail.ID)
	}
	h.press(enter, esc) // task panel and back: to the pinned list
	if h.current() != viewPinned {
		t.Fatalf("esc returned to %q", h.current())
	}
	h.press(' ', enter) // next status on a pinned task: in progress
	if got := h.fake.Task("t3").Status.Status; got != "in progress" {
		t.Fatalf("status = %q", got)
	}
	h.press('3') // back in the task list, the same task shows the change too
	for _, r := range h.app.Rows() {
		if r.Task.ID == "t3" && r.Task.Status.Status != "in progress" {
			t.Fatal("list copy of the pinned task wasn't updated")
		}
	}
	h.press('4', 'P') // unpin t3
	if len(h.app.Pinned) != 1 || h.app.Pinned[0].ID != "t1" {
		t.Fatalf("pinned = %v", h.app.Pinned)
	}
}

// W opens the timesheet: a week grid per task and day; enter edits a cell, esc goes back.
func TestTimesheetPage(t *testing.T) {
	srv := fake.Basic(5)
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)
	srv.Entries = []clickup.TimeEntry{srv.Entry("t1", time.Date(2026, 9, 21, 9, 0, 0, 0, time.Local), 90*time.Minute, "review")}
	h := newHarness(t, srv)
	h.app.Now = func() time.Time { return now }
	h.press('H')
	h.wantScreen("Timesheet", "week 39", "Mon 21", "Sun 27", "DEV-1 Task number 1", "1:30", "Total",
		"Add time: enter | Edit/delete entry: e")
	h.press('h', 'h') // today (Wed) → Mon
	h.wantScreen("[ 1:30]", "09:00–10:30  1:30  review")

	h.press(enter) // adds a new entry, even though Monday has time already
	h.wantScreen("Add time · Mon 21 Sep · Task number 1", "Day", "Mon 21 Sep 2026",
		"Start", "10:30", "End", "type a duration above") // starts where the last entry ends
	h.typeText("0:30")
	h.wantScreen("11:00  calculated") // the end follows the duration as you type
	h.press(gocui.KeyTab, 'j', enter) // Start
	h.press(gocui.KeyCtrlU)
	h.typeText("13:00")
	h.press(enter)
	h.wantScreen("13:30  calculated")
	h.press('j', 'j', enter) // Note
	h.typeText("pairing")
	h.press(enter, gocui.KeyCtrlS)
	h.wantScreen("[ 2:00]", "13:00–13:30  0:30  pairing")

	h.press('e') // two entries: pick one, then edit or delete it
	h.wantScreen("Which entry?", "1  09:00–10:30", "2  13:00–13:30")
	h.press('1')
	h.wantScreen("Edit", "Delete")
	h.press('e')
	h.wantScreen("Edit time", "09:00", "10:30  calculated", "review") // prefilled
	h.press(gocui.KeyCtrlU)
	h.typeText("2:15")
	h.wantScreen("11:15  calculated")
	h.press(enter)
	if d := time.Duration(srv.Entries[0].Duration.Int()) * time.Millisecond; d != 135*time.Minute {
		t.Fatalf("duration = %v", d)
	}
	h.wantScreen("[ 2:45]")
	h.press('e', '2', 'd') // delete the pairing entry…
	h.wantScreen("Delete time entry")
	h.press('y') // …once confirmed
	h.wantScreen("[ 2:15]")
	if len(srv.Entries) != 1 {
		t.Fatalf("entries = %d", len(srv.Entries))
	}
	h.press(esc)
	if h.gui.sheetOpen || h.current() != viewTasks {
		t.Fatalf("esc should return to the tasks panel, focus %q", h.current())
	}
}

// Pages are tabs on the top row: H the timesheet, esc back to the tasks, F2/F1, or a click.
func TestPageTabs(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.wantScreen("esc Tasks", "H Timesheet", "H timesheet · esc back")
	h.press('H')
	if !h.gui.sheetOpen || h.current() != viewSheet {
		t.Fatalf("H: sheet open %v, focus %q", h.gui.sheetOpen, h.current())
	}
	h.wantScreen("Timesheet", "Total")
	h.press(esc)
	if h.gui.sheetOpen || h.current() != viewTasks {
		t.Fatalf("esc: sheet open %v, focus %q", h.gui.sheetOpen, h.current())
	}
	h.press(gocui.KeyF2, gocui.KeyF1) // the function keys work too
	if h.gui.sheetOpen {
		t.Fatal("F2, F1 should end on the tasks page")
	}
	// Click the Timesheet tab, then the Tasks tab.
	h.mouse(viewPages, gocui.MouseLeft, 0, false)
	tab := h.gui.pageTabs[1]
	h.event(func() { _ = h.gui.pageClick(gocui.ViewMouseBindingOpts{X: tab[0] + 2}) })
	if !h.gui.sheetOpen {
		t.Fatal("clicking the Timesheet tab didn't open it")
	}
	h.event(func() { _ = h.gui.pageClick(gocui.ViewMouseBindingOpts{X: h.gui.pageTabs[0][0] + 1}) })
	if h.gui.sheetOpen {
		t.Fatal("clicking the Tasks tab didn't go back")
	}
	// A popup keeps the focus: F2 does nothing while it's open.
	h.press('s')
	h.event(func() { h.gui.showPage(pageTimesheet) })
	if h.gui.sheetOpen {
		t.Fatal("switched pages under an open popup")
	}
}

// Clicks on a panel's or popup's title border have Y = -1; they must not crash.
func TestClicksOnBordersDontCrash(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.click(viewLists, -1, false)
	h.click(viewTasks, -1, false)
	h.press('s')
	h.click(viewPopup, -1, false)
	if h.gui.popup == nil {
		t.Fatal("a click on the popup border shouldn't pick anything")
	}
}

// Deleting the only entry of the last timesheet row keeps a valid selection.
func TestTimesheetDeleteLastRowEntry(t *testing.T) {
	srv := fake.Basic(5)
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)
	monday := time.Date(2026, 9, 21, 9, 0, 0, 0, time.Local)
	srv.Entries = []clickup.TimeEntry{srv.Entry("t1", monday, time.Hour, ""), srv.Entry("t3", monday, time.Hour, "")}
	h := newHarness(t, srv)
	h.app.Now = func() time.Time { return now }
	h.press('H', 'j', 'h', 'h', 'e', 'd', 'y') // last row, Monday: edit/delete its only entry → delete, yes
	if h.app.Sheet.Row != 0 || len(srv.Entries) != 1 {
		t.Fatalf("row %d, %d entries", h.app.Sheet.Row, len(srv.Entries))
	}
}

// The cursor is only visible while typing.
func TestCursorHiddenAfterInput(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('/')
	if !h.gui.g.Cursor {
		t.Fatal("cursor should show while filtering")
	}
	h.press(enter)
	if h.gui.g.Cursor {
		t.Fatal("cursor still visible after the filter closed")
	}
	h.press('g', esc)
	if h.gui.g.Cursor {
		t.Fatal("cursor still visible after a prompt closed")
	}
}

// Clicking a property of the create form edits it; it doesn't create the task.
func TestFormClickEditsRow(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.event(func() { h.app.OpenView(app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}) })
	h.press('n')
	h.typeText("Clicked")
	h.click(viewPopup, 2, false) // Priority
	h.wantScreen("Priority", "⚑ urgent")
	if len(h.fake.Tasks) != 5 {
		t.Fatal("clicking a row created the task")
	}
}

// Quit chosen in the keybindings menu quits.
func TestQuitFromKeybindingsMenu(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('?')
	i := slices.IndexFunc(h.gui.popup.items, func(it app.MenuItem) bool { return it.Label == "Quit" })
	h.gui.popup.sel = i
	var err error
	h.event(func() { err = h.gui.popupConfirm() })
	if !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("err = %v, want ErrQuit", err)
	}
}

// A popup opening while another is open waits its turn.
func TestPopupsQueue(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('?')
	h.event(func() { h.gui.Confirm("Later", "arrived while the menu was open", func() {}) })
	if h.gui.popup.title != "Keybindings" {
		t.Fatalf("the open popup was replaced by %q", h.gui.popup.title)
	}
	h.press(esc)
	h.wantScreen("Later", "arrived while the menu was open")
}

// On short terminals the task list keeps rows; the timesheet fits 80 columns.
func TestSmallTerminal(t *testing.T) {
	async := &testAsync{cancels: map[string]context.CancelFunc{}}
	g, err := New(Options{Headless: true, Width: 80, Height: 16, Async: async})
	if err != nil {
		t.Fatal(err)
	}
	defer g.g.Close()
	c, _ := cache.Open(":memory:")
	defer c.Close()
	srv := fake.Demo()
	a := app.New(srv.Client(), c, g, async)
	a.Debounce = 0
	g.Attach(a)
	h := &harness{t: t, gui: g, app: a, fake: srv, async: async, keys: g.fixedKeys()}
	for _, b := range g.bindings {
		for _, view := range b.views {
			for _, key := range b.keys {
				h.keys[viewKey{view, key}] = b.handler
			}
		}
	}
	h.event(a.Boot)
	if v, _ := g.g.View(viewTasks); func() int { _, y := v.InnerSize(); return y }() < 1 {
		t.Fatalf("task list has no rows at 80x16:\n%s", h.screen())
	}
	h.press('H')
	h.wantScreen("Sun", "Total")
}

// The quick path: type a name, enter, pick the required Severity, and the task is created.
func TestNewTaskNameEnterWalksRequiredFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.event(func() { h.app.OpenView(app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}) })
	h.press('n')
	h.typeText("Quick one")
	h.press(enter)
	h.wantScreen("Severity", "S1", "S2")
	h.press('2')
	if h.gui.popup != nil {
		t.Fatalf("form still open:\n%s", h.screen())
	}
	if d := h.app.Detail; d.Name != "Quick one" || string(h.fake.Field(d.ID, "Severity").Value) != "1" {
		t.Fatalf("created %q, severity %s", d.Name, h.fake.Field(d.ID, "Severity").Value)
	}
}

// A typo keeps the prompt open with the problem on its border; fixing it saves.
func TestPromptStaysOpenOnATypo(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('t')
	h.press(gocui.KeyCtrlU)
	h.typeText("someday")
	h.press(enter)
	h.wantScreen("Due date", `can't read "someday" as a date`)
	h.press('!') // typing clears the message
	if strings.Contains(h.screen(), "can't read") {
		t.Fatal("the message should go once you type")
	}
	h.press(gocui.KeyCtrlU)
	h.typeText("tomorrow")
	h.press(enter)
	if h.gui.popup != nil || h.fake.Task("t1").DueDate == "" {
		t.Fatalf("popup %v, due %q", h.gui.popup, h.fake.Task("t1").DueDate)
	}
}

// A form shows its problems itself, instead of a message in the corner.
func TestFormErrorShowsInTheForm(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('L')
	h.wantScreen("Log time · DEV-1", "so that it ends now")
	h.typeText("lots")
	h.press(enter)
	h.wantScreen("Log time · DEV-1", "start with a duration")
	h.press(gocui.KeyCtrlU)
	h.typeText("45m fixed it")
	h.wantScreen("fixed it", "calculated")
	h.press(enter)
	if h.gui.popup != nil || len(h.fake.Entries) != 1 || h.fake.Entries[0].Description != "fixed it" {
		t.Fatalf("popup %v, entries %+v", h.gui.popup, h.fake.Entries)
	}
}

// tab completes an @mention in a comment.
func TestCommentMentionCompletes(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('c')
	h.typeText("hi @al")
	h.press(gocui.KeyTab)
	if got := h.gui.inputText(); got != "hi @alice " {
		t.Fatalf("input = %q", got)
	}
	h.typeText("look")
	h.press(enter)
	h.wantScreen("@alice look", "Comment posted on DEV-1")
	if len(h.fake.Mentions) != 1 {
		t.Fatalf("mentions = %+v", h.fake.Mentions)
	}
}

// S sorts the list; a heading shows each group and how many tasks it has.
func TestSortHeadings(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.wantScreen("● TO DO  3")
	h.press('S', '2') // by assignee
	h.wantScreen("tester  3")
	h.press('S', '4') // by due date
	h.wantScreen("No due date  3")
}

// A prompt opened from a picker gets the typing: both use the input view, which is
// recreated in between. Here: a timesheet row for a task by its custom id.
func TestTimesheetAddRowByID(t *testing.T) {
	h := newHarness(t, fake.Basic(5))
	h.press('H', 'n')
	h.wantScreen("Add a task row", "+ another task by id or URL")
	h.press(enter) // the first option
	h.wantScreen("task id, custom id (ABC-123) or URL")
	h.typeText("DEV-4")
	if got := h.gui.inputText(); got != "DEV-4" {
		t.Fatalf("the prompt got %q: typing went elsewhere", got)
	}
	h.press(enter)
	h.wantScreen("DEV-4 Task number 4")
	if rows, _, _ := h.app.SheetRows(); rows[h.app.Sheet.Row].TaskID != "t4" {
		t.Fatalf("selected row %+v", rows[h.app.Sheet.Row])
	}
}

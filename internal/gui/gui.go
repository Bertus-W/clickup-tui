// Package gui is the lazygit-style terminal UI, built on jesseduffield/gocui.
//
//	╭[1] Workspace──────────╮╭[4] Task─────────────────────╮
//	╰───────────────────────╯│ title, meta, custom fields, │
//	╭[2] Lists──────────────╮│ description, comments       │
//	╰───────────────────────╯╰─────────────────────────────╯
//	╭[3] Backlog - Mine─────╮╭Command log──────────────────╮
//	╰────────────── 3 of 16╯╰─────────────────────────────╯
//	Status: s | Done: space | Fields: f | …   Keybindings: ?
package gui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

const (
	viewStatus     = "status"
	viewLists      = "lists"
	viewTasks      = "tasks"
	viewPinned     = "pinned"
	viewDetail     = "detail"
	viewLog        = "log"
	viewOptions    = "options"
	viewInfo       = "info"
	viewFilter     = "filter"
	viewPopup      = "popup"
	viewPopupInput = "popupInput"
)

// panels in h/l order; numbered like lazygit, with the main (task) panel as 0.
var panels = []string{viewStatus, viewLists, viewTasks, viewPinned, viewDetail}

var spinner = []rune("⣾⣽⣻⢿⡿⣟⣯⣷")

// Selected line colours: lazygit's blue bar in the focused panel, a grey bar elsewhere so
// you can still see where each panel's cursor is.
var (
	selectedBg         = gocui.ColorBlue
	inactiveSelectedBg = gocui.NewRGBColor(0x44, 0x47, 0x5a)
)

type screenMode int

const (
	modeNormal screenMode = iota
	modeHalf
	modeFull
)

func (m screenMode) String() string { return [...]string{"normal", "half", "full"}[m] }

type Gui struct {
	g     *gocui.Gui
	App   *app.App
	async app.Async

	panel        string // focused panel (also while a popup is open)
	lastList     string // tasks or pinned: where esc in the task panel returns to
	mode         screenMode
	showLog      bool
	filtering    bool
	filterFilled bool
	popup        *popup
	returnTo     *popup // a form waiting for one of its property editors to close
	sheetOpen    bool   // the timesheet page replaces the panels
	sheetPrev    string // panel to return to when it closes
	bindings     []binding

	tree        []treeLine
	treeSel     int
	expanded    map[string]bool
	treeOpened  bool
	shownDetail string

	toast      string
	toastLevel app.Level
	toastUntil time.Time
	spin       int

	// editor opens text in $EDITOR; tests replace it.
	editor func(initial string) (string, bool, error)
}

type Options struct {
	Headless      bool // for tests: render to an in-memory screen
	Width, Height int
	Async         app.Async // defaults to the gocui event loop
}

// New creates the GUI; call Attach with the app, then Run.
func New(opts Options) (*Gui, error) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{
		OutputMode: gocui.OutputTrue,
		Headless:   opts.Headless,
		Width:      opts.Width,
		Height:     opts.Height,
	})
	if err != nil {
		return nil, err
	}
	gui := &Gui{g: g, panel: viewTasks, lastList: viewTasks, showLog: true, expanded: map[string]bool{}}
	gui.editor = gui.runEditor
	gui.async = opts.Async
	if gui.async == nil {
		gui.async = newAsync(g)
	}
	g.Highlight = true
	g.ShowListFooter = true
	g.SelFgColor = gocui.ColorGreen | gocui.AttrBold
	g.SelFrameColor = gocui.ColorGreen
	g.FrameColor = gocui.ColorDefault
	g.Cursor = false
	g.SetManagerFunc(gui.layout)
	gui.bindings = append(gui.keymap(), gui.sheetKeys()...)
	if err := gui.register(); err != nil {
		return nil, err
	}
	if err := gui.registerMouse(); err != nil {
		return nil, err
	}
	return gui, nil
}

// Async is what the app uses to schedule work; see app.Async.
func (gui *Gui) Async() app.Async { return gui.async }

func (gui *Gui) Attach(a *app.App) { gui.App = a }

// Run starts the event loop until the user quits.
func (gui *Gui) Run() error {
	defer gui.g.Close()
	gui.App.Boot()
	go gui.ticker()
	go gui.clockTick()
	err := gui.g.MainLoop()
	if errors.Is(err, gocui.ErrQuit) {
		return nil
	}
	return err
}

// ticker animates the spinner while busy and triggers the periodic refresh.
func (gui *Gui) ticker() {
	spin := time.NewTicker(100 * time.Millisecond)
	refresh := time.NewTicker(time.Minute)
	for {
		select {
		case <-spin.C:
			if gui.App.Busy() {
				gui.async.UI(func() { gui.spin++ })
			}
		case <-refresh.C:
			gui.async.UI(func() {
				if gui.popup == nil {
					gui.App.AutoRefresh()
				}
			})
		}
	}
}

// --- layout -------------------------------------------------------------------------------

func (gui *Gui) layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	bottom := maxY - 2 // last row panels may use; the row below holds key hints
	if gui.sheetOpen {
		if err := gui.layoutSheet(maxX, maxY); err != nil {
			return err
		}
		return gui.finishLayout(g, maxX, maxY)
	}
	for _, name := range []string{viewSheet, viewSheetSide} {
		if v, err := g.View(name); err == nil {
			v.Visible = false
		}
	}

	focusedLeft := gui.panel != viewDetail
	leftW := maxX * 55 / 100
	switch gui.mode {
	case modeHalf:
		leftW = maxX * 30 / 100
		if focusedLeft {
			leftW = maxX * 70 / 100
		}
	case modeFull:
		leftW = 0
		if focusedLeft {
			leftW = maxX
		}
	}

	type box struct{ x0, y0, x1, y1 int }
	boxes := map[string]box{}
	if gui.mode == modeFull {
		boxes[gui.panel] = box{0, 0, maxX - 1, bottom}
	} else {
		pinnedH := min(max(len(gui.App.Pinned), 1), 8) + 2
		listsH := max(5, (bottom-4-pinnedH)/3)
		boxes[viewStatus] = box{0, 0, leftW - 1, 3}
		boxes[viewLists] = box{0, 4, leftW - 1, 4 + listsH - 1}
		boxes[viewTasks] = box{0, 4 + listsH, leftW - 1, bottom - pinnedH}
		boxes[viewPinned] = box{0, bottom - pinnedH + 1, leftW - 1, bottom}
		logH := 0
		if gui.showLog {
			logH = min(9, bottom/3)
		}
		boxes[viewDetail] = box{leftW, 0, maxX - 1, bottom - logH}
		if logH > 0 {
			boxes[viewLog] = box{leftW, bottom - logH + 1, maxX - 1, bottom}
		}
	}
	for _, name := range append(slices.Clone(panels), viewLog) {
		b, ok := boxes[name]
		if !ok {
			b = box{0, 0, 1, 1}
		}
		v, err := gui.view(name, b.x0, b.y0, b.x1, b.y1)
		if err != nil {
			return err
		}
		v.Visible = ok
	}

	// gocui doesn't track this itself: HighlightInactive picks the colour unconditionally.
	for _, name := range []string{viewLists, viewTasks, viewPinned} {
		v, _ := g.View(name)
		v.HighlightInactive = gui.popup != nil || gui.panel != name
	}

	gui.renderStatus()
	gui.renderLists()
	gui.renderTasks()
	gui.renderPinned()
	gui.renderDetail()
	gui.renderLog()
	return gui.finishLayout(g, maxX, maxY)
}

// finishLayout draws the hint line and any popup, and puts the focus where it belongs.
func (gui *Gui) finishLayout(g *gocui.Gui, maxX, maxY int) error {
	if err := gui.renderBottom(maxX, maxY); err != nil {
		return err
	}
	if err := gui.layoutPopup(maxX, maxY); err != nil {
		return err
	}
	if gui.popup == nil {
		name := gui.panel
		if gui.filtering {
			name = viewFilter
		}
		if cur := g.CurrentView(); cur == nil || cur.Name() != name {
			if _, err := g.SetCurrentView(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// view creates or moves a view, configuring it the first time.
func (gui *Gui) view(name string, x0, y0, x1, y1 int) (*gocui.View, error) {
	v, err := gui.g.SetView(name, x0, y0, x1, y1, 0)
	switch {
	case isUnknownView(err): // just created
		gui.initView(v)
	case err != nil:
		return nil, err
	}
	v.Visible = true
	return v, nil
}

// isUnknownView matches gocui's "view was just created" signal. gocui wraps it with
// go-errors v1.0.2, which predates Unwrap, so errors.Is can't see through it.
func isUnknownView(err error) bool {
	return err != nil && (errors.Is(err, gocui.ErrUnknownView) || err.Error() == gocui.ErrUnknownView.Error())
}

func (gui *Gui) initView(v *gocui.View) {
	v.FrameRunes = []rune{'─', '│', '╭', '╮', '╰', '╯'}
	v.SelBgColor = selectedBg
	v.InactiveViewSelBgColor = inactiveSelectedBg
	switch v.Name() {
	case viewStatus:
		v.TitlePrefix = "[1]"
		v.Title = "Workspace"
	case viewLists:
		v.TitlePrefix = "[2]"
		v.Title = "Lists"
		v.Highlight = true
	case viewTasks:
		v.TitlePrefix = "[3]"
		v.Highlight = true
	case viewPinned:
		v.TitlePrefix = "[4]"
		v.Title = "Pinned"
		v.Highlight = true
	case viewDetail:
		v.TitlePrefix = "[0]"
		v.Title = "Task"
		v.Wrap = true
	case viewLog:
		v.Title = "Command log"
		v.Autoscroll = true
	case viewOptions, viewInfo:
		v.Frame = false
	case viewFilter:
		v.Frame = false
		v.Editable = true
		v.Editor = gocui.EditorFunc(gui.editFilter)
	case viewSheet:
		v.Highlight = true
	case viewSheetSide:
		v.Wrap = true
	case viewPopup:
		v.Highlight = true
		v.Wrap = true
	case viewPopupInput:
		v.Editable = true
		v.Editor = gocui.EditorFunc(gui.editPopupInput)
	}
}

func write(v *gocui.View, lines []string) {
	v.Clear()
	fmt.Fprint(v, strings.Join(lines, "\n"))
}

// --- panels -----------------------------------------------------------------------------------

func (gui *Gui) renderStatus() {
	v, _ := gui.g.View(viewStatus)
	a := gui.App
	check := "  "
	if !a.SyncedAt.IsZero() {
		check = style.Green("✓ ")
	}
	line1 := check + style.Bold(cmp.Or(a.Team().Name, "…")) + " → " + style.Cyan(cmp.Or(a.Me.Username, "…"))
	if timer := a.TimerLine(); timer != "" {
		line1 += "   " + timer
	}
	line2 := style.Dim("cached")
	switch {
	case a.Busy():
		line2 = style.Yellow(string(spinner[gui.spin%len(spinner)]) + " syncing")
	case !a.SyncedAt.IsZero():
		line2 = style.Dim("synced " + a.SyncedAt.Format("15:04:05"))
	}
	write(v, []string{line1, line2})
}

type treeLine struct {
	key    string
	depth  int
	label  string
	branch bool
	view   *app.View
}

func (gui *Gui) buildTree() []treeLine {
	a := gui.App
	if !gui.treeOpened && a.LastList != nil && len(a.Hierarchy) > 0 {
		gui.treeOpened = true
		for _, s := range a.Hierarchy {
			for _, l := range s.Lists {
				if l.ID == a.LastList.ID {
					gui.expanded["s:"+s.ID] = true
				}
			}
			for _, f := range s.Folders {
				for _, l := range f.Lists {
					if l.ID == a.LastList.ID {
						gui.expanded["s:"+s.ID], gui.expanded["f:"+f.ID] = true, true
					}
				}
			}
		}
	}
	listLine := func(l clickup.ListRef, depth int) treeLine {
		return treeLine{key: "l:" + l.ID, depth: depth, label: l.Name, view: &app.View{Kind: "list", ID: l.ID, Name: l.Name}}
	}
	lines := []treeLine{{key: "my", label: "★ My tasks", view: &app.MyTasks}}
	for _, s := range a.Hierarchy {
		lines = append(lines, treeLine{key: "s:" + s.ID, label: s.Name, branch: true})
		if !gui.expanded["s:"+s.ID] {
			continue
		}
		for _, f := range s.Folders {
			lines = append(lines, treeLine{key: "f:" + f.ID, depth: 1, label: f.Name, branch: true})
			if gui.expanded["f:"+f.ID] {
				for _, l := range f.Lists {
					lines = append(lines, listLine(l, 2))
				}
			}
		}
		for _, l := range s.Lists {
			lines = append(lines, listLine(l, 1))
		}
	}
	return lines
}

func (gui *Gui) renderLists() {
	v, _ := gui.g.View(viewLists)
	gui.tree = gui.buildTree()
	gui.treeSel = min(max(gui.treeSel, 0), len(gui.tree)-1)
	lines := make([]string, len(gui.tree))
	for i, l := range gui.tree {
		indent := strings.Repeat("  ", l.depth)
		switch {
		case l.branch && gui.expanded[l.key]:
			lines[i] = indent + "▼ " + style.Bold(l.label)
		case l.branch:
			lines[i] = indent + "▶ " + style.Bold(l.label)
		case l.view.Kind == "list" && gui.App.View.ID == l.view.ID,
			l.view.Kind == "my" && gui.App.View.Kind == "my":
			lines[i] = indent + "  " + style.Green(l.label)
		default:
			lines[i] = indent + "  " + l.label
		}
	}
	if gui.treeSel >= 0 {
		lines[gui.treeSel] = style.Strip(lines[gui.treeSel]) // plain text reads best on the selection bar
	}
	write(v, lines)
	v.FocusPoint(0, gui.treeSel, true)
	v.Footer = fmt.Sprintf("%d of %d", gui.treeSel+1, len(gui.tree))
}

func (gui *Gui) renderTasks() {
	v, _ := gui.g.View(viewTasks)
	a := gui.App
	listName := "List"
	if a.LastList != nil {
		listName = a.LastList.Name
	}
	v.Tabs = []string{listName, "Mine"}
	v.TabIndex = 0
	if a.View.Kind == "my" {
		v.TabIndex = 1
	}
	var sub []string
	if a.IncludeClosed {
		sub = append(sub, "+closed")
	}
	if a.Filter != "" {
		sub = append(sub, "filter: "+a.Filter)
	}
	v.Subtitle = strings.Join(sub, " · ")

	rows := a.Rows()
	width, _ := v.InnerSize()
	table := render.NewTable(a.Tasks, width, a.View.Kind == "my")
	now := a.Now()
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = table.Line(r, now)
	}
	if len(rows) == 0 {
		switch {
		case a.Loading:
			lines = []string{style.Dim("loading…")}
		case a.Filter != "":
			lines = []string{style.Dim("no tasks match the filter")}
		default:
			lines = []string{style.Dim("no tasks")}
		}
	}
	sel := max(a.SelectedIndex(), 0)
	if len(rows) > 0 {
		lines[sel] = style.Strip(lines[sel]) // plain text reads best on the selection bar
	}
	write(v, lines)
	v.FocusPoint(0, sel, true)
	v.Footer = ""
	if len(rows) > 0 {
		v.Footer = fmt.Sprintf("%d of %d", sel+1, len(rows))
	}
}

func (gui *Gui) renderPinned() {
	v, _ := gui.g.View(viewPinned)
	a := gui.App
	v.Highlight = len(a.Pinned) > 0 // no selection bar on the empty-state hint
	if len(a.Pinned) == 0 {
		v.Footer = ""
		write(v, []string{style.Dim("P pins the selected task here, from any list")})
		return
	}
	width, _ := v.InnerSize()
	table := render.NewTable(a.Pinned, width, true)
	now := a.Now()
	lines := make([]string, len(a.Pinned))
	for i, t := range a.Pinned {
		lines[i] = table.Line(render.Row{Task: t}, now)
	}
	lines[a.PinnedSel] = style.Strip(lines[a.PinnedSel]) // plain text reads best on the selection bar
	write(v, lines)
	v.FocusPoint(0, a.PinnedSel, true)
	v.Footer = fmt.Sprintf("%d of %d", a.PinnedSel+1, len(a.Pinned))
}

func (gui *Gui) renderDetail() {
	v, _ := gui.g.View(viewDetail)
	t := gui.App.Detail
	if t == nil {
		v.Subtitle = ""
		write(v, []string{"", style.Dim("No task selected")})
		return
	}
	if t.ID != gui.shownDetail {
		gui.shownDetail = t.ID
		v.SetOrigin(0, 0)
	}
	v.Subtitle = t.Label()
	write(v, strings.Split(render.Detail(t, gui.App.Comments, gui.App.Now()), "\n"))
}

func (gui *Gui) renderLog() {
	v, _ := gui.g.View(viewLog)
	write(v, gui.App.Log)
}

func (gui *Gui) renderBottom(maxX, maxY int) error {
	info := ""
	if time.Now().Before(gui.toastUntil) {
		info = gui.toast
		switch gui.toastLevel {
		case app.Error:
			info = style.Red(info)
		case app.Warn:
			info = style.Yellow(info)
		default:
			info = style.Green(info)
		}
	} else if gui.App.Busy() {
		info = style.Yellow(string(spinner[gui.spin%len(spinner)]) + " syncing")
	}
	// The hint line always spans the full width (gocui doesn't repaint cells no view
	// covers); the info area is laid over its right end only while it has something to say.
	infoW := min(style.Width(info), maxX/2)
	opts, err := gui.view(viewOptions, -1, maxY-2, maxX, maxY)
	if err != nil {
		return err
	}
	infoV, err := gui.view(viewInfo, maxX-infoW-2, maxY-2, maxX, maxY)
	if err != nil {
		return err
	}
	infoV.Visible = infoW > 0
	write(infoV, []string{info})
	if infoW > 0 {
		infoW += 2 // keep a gap between hints and info
	}

	if gui.filtering {
		write(opts, []string{style.Cyan("Filter:")})
		f, err := gui.view(viewFilter, 7, maxY-2, maxX-infoW-1, maxY)
		if err != nil {
			return err
		}
		if !gui.filterFilled {
			gui.filterFilled = true
			f.TextArea.Clear()
			f.TextArea.TypeString(gui.App.Filter)
			f.RenderTextArea()
		}
		gui.g.Cursor = gui.popup == nil
		return nil
	}
	if f, err := gui.g.View(viewFilter); err == nil {
		f.Visible = false
	}
	write(opts, []string{gui.hints(maxX - infoW - 1)})
	return nil
}

// --- focus and screen modes -------------------------------------------------------------------

func (gui *Gui) Focus(p app.Panel) {
	gui.focus(map[app.Panel]string{
		app.PanelWorkspace: viewStatus, app.PanelLists: viewLists, app.PanelTasks: viewTasks,
		app.PanelPinned: viewPinned, app.PanelDetail: viewDetail,
	}[p])
}

// focus moves to a panel; the task panel follows whichever task list you're in.
func (gui *Gui) focus(name string) {
	gui.panel = name
	gui.filtering = false
	switch name {
	case viewTasks:
		gui.lastList = name
		if gui.App != nil {
			gui.App.Select(max(gui.App.SelectedIndex(), 0))
		}
	case viewPinned:
		gui.lastList = name
		if gui.App != nil {
			gui.App.SelectPinned(gui.App.PinnedSel)
		}
	}
}

func (gui *Gui) cyclePanel(delta int) {
	i := slices.Index(panels, gui.panel)
	gui.focus(panels[(i+delta+len(panels))%len(panels)])
}

// Notify shows a warning ("can't do that here") as a small popup, like lazygit, unless
// another popup is open; info and errors appear at the bottom right and in the command log.
func (gui *Gui) Notify(level app.Level, msg string) {
	if level == app.Warn && gui.popup == nil {
		gui.open(&popup{kind: popupInfo, title: "Heads up", message: msg})
		return
	}
	gui.toast, gui.toastLevel = msg, level
	d := 3 * time.Second
	if level == app.Error {
		d = 8 * time.Second
	}
	gui.toastUntil = time.Now().Add(d)
	time.AfterFunc(d, func() { gui.async.UI(func() {}) }) // redraw when it expires
}

func (gui *Gui) Refresh() {} // gocui redraws after every event and UI update

func (gui *Gui) Clipboard(text string) error { return copyToClipboard(text) }
func (gui *Gui) OpenURL(url string) error    { return openURL(url) }

func (gui *Gui) Edit(title, initial string, onDone func(string)) {
	text, ok, err := gui.editor(initial)
	if err != nil {
		gui.Notify(app.Error, title+": "+err.Error())
		return
	}
	if ok {
		onDone(text)
	}
}

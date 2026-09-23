package gui

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/jesseduffield/gocui"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

type popupKind int

const (
	popupMenu popupKind = iota
	popupPicker
	popupMulti
	popupPrompt
	popupConfirm
	popupInfo // a message you dismiss with enter or esc
	popupForm // new task: name input plus a list of properties
)

// popup is the one open dialog. Callbacks run after it closes, so they may open the next one.
type popup struct {
	kind      popupKind
	title     string
	sel       int
	items     []app.MenuItem // menu
	keyLabels []string       // menu: key column text, when keys aren't plain runes
	options   []app.Option   // picker, multi
	visible   []int          // indexes into options after filtering
	chosen    map[string]bool
	query     string
	typing    bool // multi: the filter input has focus
	prompt    app.Prompt
	message   string
	filled    bool // the input view was prefilled

	form       *app.Form
	inList     bool // form: focus is on the properties, not the name
	autoSubmit bool // form: enter found empty required fields; create once they're filled
	editDone   bool // form: the last property editor finished (wasn't cancelled)

	onMenu    func(app.MenuItem)
	onPick    func(string)
	onMulti   func(map[string]bool)
	onPrompt  func(string)
	onConfirm func()
}

func (gui *Gui) open(p *popup) {
	if p.options != nil {
		p.refilter("")
	}
	gui.popup = p
}

// close removes the popup views; callers run the popup's callback afterwards.
func (gui *Gui) close() {
	gui.popup = nil
	_ = gui.g.DeleteView(viewPopup)
	_ = gui.g.DeleteView(viewPopupInput)
}

func (p *popup) refilter(query string) {
	p.query = query
	p.visible = p.visible[:0]
	q := strings.ToLower(query)
	for i, o := range p.options {
		if strings.Contains(strings.ToLower(style.Strip(o.Label)), q) {
			p.visible = append(p.visible, i)
		}
	}
	p.sel = min(p.sel, max(len(p.visible)-1, 0))
	if query != "" {
		p.sel = 0
	}
}

func (p *popup) count() int {
	switch p.kind {
	case popupMenu:
		return len(p.items)
	case popupForm:
		return len(p.form.Rows())
	}
	return len(p.visible)
}

// popupMove moves the selection; in a form, the first move from the name enters the list.
func (gui *Gui) popupMove(delta int) {
	p := gui.popup
	if p.kind == popupForm && !p.inList {
		p.form.Name = gui.inputText()
		p.inList = true
		return
	}
	p.move(delta)
}

// popupTab switches between a form's name and its properties; elsewhere it moves down.
func (gui *Gui) popupTab() {
	p := gui.popup
	if p.kind != popupForm {
		p.move(1)
		return
	}
	if !p.inList {
		p.form.Name = gui.inputText()
	}
	p.inList = !p.inList
}

func (p *popup) move(delta int) {
	if n := p.count(); n > 0 {
		p.sel = (p.sel + delta + n) % n
	}
}

// --- app.UI --------------------------------------------------------------------------------

func (gui *Gui) Menu(title string, items []app.MenuItem, highlighted int, onPick func(app.MenuItem)) {
	gui.open(&popup{kind: popupMenu, title: title, items: items, sel: highlighted, onMenu: onPick})
}

func (gui *Gui) Pick(title string, options []app.Option, current string, onPick func(string)) {
	p := &popup{kind: popupPicker, title: title, options: options, onPick: onPick}
	p.sel = max(slices.IndexFunc(options, func(o app.Option) bool { return o.ID == current }), 0)
	gui.open(p)
}

func (gui *Gui) MultiPick(title string, options []app.Option, selected map[string]bool, onDone func(map[string]bool)) {
	gui.open(&popup{kind: popupMulti, title: title, options: options, chosen: maps.Clone(selected), onMulti: onDone})
}

func (gui *Gui) Prompt(p app.Prompt, onSubmit func(string)) {
	gui.open(&popup{kind: popupPrompt, title: p.Title, prompt: p, onPrompt: onSubmit})
}

func (gui *Gui) Confirm(title, message string, onYes func()) {
	gui.open(&popup{kind: popupConfirm, title: title, message: message, onConfirm: onYes})
}

func (gui *Gui) Form(f *app.Form) {
	gui.returnTo = nil
	gui.open(&popup{kind: popupForm, title: f.Title, form: f})
}

// formLines renders the properties: "* Severity   required", required ones marked.
func formLines(f *app.Form) []string {
	rows := f.Rows()
	width := 0
	for _, r := range rows {
		width = max(width, len([]rune(r.Label)))
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		mark := "  "
		if r.Required {
			mark = style.Red("* ")
		}
		lines[i] = mark + style.Cell{Text: r.Label}.Fit(width) + "  " + r.Value
	}
	return lines
}

// formSubmit creates the task, or walks the user through the first empty required field.
func (gui *Gui) formSubmit() error {
	p := gui.popup
	if !p.inList {
		p.form.Name = gui.inputText()
	}
	missing, ok := p.form.Submit(p.form.Name)
	switch {
	case ok:
		gui.returnTo = nil
		gui.close()
	case missing >= 0:
		p.autoSubmit = true
		gui.formEdit(missing)
	}
	return nil
}

// formEdit swaps the form for the editor of row i; resumeForm brings the form back.
func (gui *Gui) formEdit(i int) {
	p := gui.popup
	if !p.inList {
		p.form.Name = gui.inputText()
	}
	rows := p.form.Rows()
	if i < 0 || i >= len(rows) {
		return
	}
	p.sel, p.editDone = i, false
	gui.close()
	gui.returnTo = p
	rows[i].Edit(func() { p.editDone = true })
	gui.resumeForm()
}

// resumeForm reopens the form once the property editor (or a message it caused) closed.
func (gui *Gui) resumeForm() {
	p := gui.returnTo
	if p == nil || gui.popup != nil {
		return
	}
	gui.returnTo = nil
	p.filled = false
	p.prompt.Value = p.form.Name
	gui.open(p)
	if p.editDone && p.autoSubmit {
		_ = gui.formSubmit() // continue with the next required field, or create
		return
	}
	p.autoSubmit = false // a cancelled editor stops the walk-through
}

// --- layout ----------------------------------------------------------------------------------

func (gui *Gui) layoutPopup(maxX, maxY int) error {
	p := gui.popup
	if p == nil {
		return nil
	}
	lines := gui.popupLines(p)
	// Title and key hints share the top border, so leave room for both.
	contentW := style.Width(p.title) + style.Width(popupHint(p)) + 6
	for _, l := range lines {
		contentW = max(contentW, style.Width(l))
	}
	w := min(maxX-4, max(56, contentW+4))
	x0 := (maxX - w) / 2
	listH := min(max(len(lines), 1), maxY-8)

	hasInput := p.kind == popupPicker || p.kind == popupPrompt || p.kind == popupForm || (p.kind == popupMulti && p.typing)
	hasList := p.kind != popupPrompt
	height := 0
	if hasInput {
		height += 3
	}
	if hasList {
		height += listH + 2
	}
	y := max((maxY-height)/2, 0)

	current := viewPopup
	if hasInput {
		in, err := gui.view(viewPopupInput, x0, y, x0+w, y+2)
		if err != nil {
			return err
		}
		in.Title = p.title
		in.Subtitle = popupHint(p)
		if !p.filled {
			p.filled = true
			in.TextArea.Clear()
			in.TextArea.TypeString(cmp.Or(p.prompt.Value, p.query))
			in.RenderTextArea()
		}
		gui.g.Cursor = !p.inList
		current = viewPopupInput
		if p.inList {
			current = viewPopup
		}
		y += 3
	} else {
		gui.g.Cursor = false
		_ = gui.g.DeleteView(viewPopupInput)
	}
	if hasList {
		v, err := gui.view(viewPopup, x0, y, x0+w, y+listH+1)
		if err != nil {
			return err
		}
		v.Title = ""
		if !hasInput {
			v.Title = p.title
		}
		v.Subtitle = map[popupKind]string{
			popupMenu:    "enter: select · esc: cancel",
			popupPicker:  "↑↓: move · enter: select · esc: cancel",
			popupMulti:   "space: toggle · /: filter · enter: done · esc: cancel",
			popupConfirm: "enter: confirm · esc: cancel",
			popupInfo:    "enter/esc: close",
			popupForm:    "enter: edit · tab: name · ctrl+s: create",
		}[p.kind]
		v.Highlight = p.kind != popupConfirm && p.kind != popupInfo && (p.kind != popupForm || p.inList)
		if v.Highlight && p.sel < len(lines) {
			lines[p.sel] = style.Strip(lines[p.sel]) // plain text reads best on the selection bar
		}
		write(v, lines)
		v.FocusPoint(0, p.sel, true)
		if _, err := gui.g.SetViewOnTop(viewPopup); err != nil {
			return err
		}
	}
	if hasInput {
		if _, err := gui.g.SetViewOnTop(viewPopupInput); err != nil {
			return err
		}
	}
	if !hasInput && p.kind == popupMulti {
		_ = gui.g.DeleteView(viewPopupInput)
	}
	if cur := gui.g.CurrentView(); cur == nil || cur.Name() != current {
		_, err := gui.g.SetCurrentView(current)
		return err
	}
	return nil
}

// popupHint is the key hint on the input's border (prompts show their own hint there).
func popupHint(p *popup) string {
	if p.kind == popupForm {
		return "enter: create · tab: fields · esc: cancel"
	}
	return p.prompt.Hint
}

func (gui *Gui) popupLines(p *popup) []string {
	switch p.kind {
	case popupMenu:
		width := 0
		keys := make([]string, len(p.items))
		for i, it := range p.items {
			keys[i] = keyName(it.Key)
			if p.keyLabels != nil {
				keys[i] = p.keyLabels[i]
			}
			width = max(width, len([]rune(keys[i])))
		}
		lines := make([]string, len(p.items))
		for i, it := range p.items {
			lines[i] = style.BoldCyan(keys[i]+strings.Repeat(" ", width-len([]rune(keys[i])))) + "  " + it.Label
		}
		return lines
	case popupPicker, popupMulti:
		if len(p.visible) == 0 {
			return []string{style.Dim("no matches")}
		}
		lines := make([]string, len(p.visible))
		for i, idx := range p.visible {
			o := p.options[idx]
			lines[i] = o.Label
			if p.kind == popupMulti {
				box := "[ ] "
				if p.chosen[o.ID] {
					box = style.Green("[x] ")
				}
				lines[i] = box + o.Label
			}
		}
		return lines
	case popupConfirm, popupInfo:
		return strings.Split(p.message, "\n")
	case popupForm:
		return formLines(p.form)
	}
	return nil
}

// --- interaction -------------------------------------------------------------------------------

func (gui *Gui) popupConfirm() error {
	defer gui.resumeForm()
	p := gui.popup
	switch p.kind {
	case popupForm:
		if p.inList {
			gui.formEdit(p.sel)
			return nil
		}
		return gui.formSubmit()
	case popupMenu:
		if p.sel < len(p.items) {
			gui.close()
			p.onMenu(p.items[p.sel])
		}
	case popupPicker:
		if len(p.visible) > 0 {
			gui.close()
			p.onPick(p.options[p.visible[p.sel]].ID)
		}
	case popupMulti:
		if p.typing { // enter in the filter returns to the list
			p.typing = false
			return nil
		}
		gui.close()
		p.onMulti(p.chosen)
	case popupPrompt:
		text := strings.TrimSpace(gui.inputText())
		gui.close()
		if text != "" || p.prompt.AllowEmpty {
			p.onPrompt(text)
		}
	case popupConfirm:
		gui.close()
		p.onConfirm()
	case popupInfo:
		gui.close()
	}
	return nil
}

func (gui *Gui) popupCancel() error {
	defer gui.resumeForm()
	if p := gui.popup; p.kind == popupForm {
		if p.inList { // back to the name
			p.inList = false
			return nil
		}
		gui.returnTo = nil
	}
	if p := gui.popup; p.kind == popupMulti && p.typing {
		p.typing = false
		p.filled = false
		p.refilter("")
		return nil
	}
	gui.close()
	return nil
}

// popupRune handles letter keys on a list popup: menu hotkeys, j/k, space and /.
func (gui *Gui) popupRune(r rune) error {
	p := gui.popup
	switch {
	case r == 'j':
		p.move(1)
	case r == 'k':
		p.move(-1)
	case p.kind == popupMenu:
		if i := slices.IndexFunc(p.items, func(it app.MenuItem) bool { return it.Key == r }); i >= 0 {
			p.sel = i
			return gui.popupConfirm()
		}
	case p.kind == popupForm && r == ' ':
		gui.formEdit(p.sel)
	case p.kind == popupMulti && r == ' ':
		if len(p.visible) > 0 {
			id := p.options[p.visible[p.sel]].ID
			p.chosen[id] = !p.chosen[id]
		}
	case p.kind == popupMulti && r == '/':
		p.typing, p.filled = true, false
	case p.kind == popupConfirm && r == 'y':
		return gui.popupConfirm()
	case p.kind == popupConfirm && r == 'n':
		return gui.popupCancel()
	}
	return nil
}

func (gui *Gui) inputText() string {
	v, err := gui.g.View(viewPopupInput)
	if err != nil {
		return ""
	}
	return v.TextArea.GetContent()
}

// editPopupInput types into a popup's input and refilters its list as you type.
func (gui *Gui) editPopupInput(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) bool {
	handled := gocui.SimpleEditor(v, key, ch, mod)
	if p := gui.popup; p != nil && p.options != nil {
		p.refilter(v.TextArea.GetContent())
	}
	return handled
}

func (gui *Gui) editFilter(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) bool {
	handled := gocui.SimpleEditor(v, key, ch, mod)
	gui.App.SetFilter(strings.TrimSpace(v.TextArea.GetContent()))
	return handled
}

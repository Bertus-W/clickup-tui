package app_test

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// testAsync runs work in goroutines and serialises UI callbacks with a mutex.
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

// scriptUI answers prompts from a queue: a rune picks a menu item, a string picks the
// option whose id or label contains it (or is typed into a prompt), a []string toggles
// options in a multi-picker, a bool answers a confirmation and nil cancels.
type scriptUI struct {
	t           *testing.T
	highlighted int
	answers     []any
	titles      []string
	notes       []string
	focus       app.Panel
	form        *app.Form
	clipboard   string
	editorText  string // what the editor last opened with
}

func (s *scriptUI) next(title string) any {
	s.titles = append(s.titles, title)
	if len(s.answers) == 0 {
		s.t.Fatalf("unexpected popup %q", title)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func matches(o app.Option, want string) bool {
	return o.ID == want || strings.Contains(strings.ToLower(style.Strip(o.Label)), strings.ToLower(want))
}

func (s *scriptUI) Menu(title string, items []app.MenuItem, highlighted int, onPick func(app.MenuItem)) {
	s.highlighted = highlighted
	switch answer := s.next(title).(type) {
	case string: // "enter": take the preselected item
		onPick(items[highlighted])
	case rune:
		key := answer
		i := slices.IndexFunc(items, func(it app.MenuItem) bool { return it.Key == key })
		if i < 0 {
			s.t.Fatalf("menu %q has no key %q", title, key)
		}
		onPick(items[i])
	}
}

func (s *scriptUI) Pick(title string, options []app.Option, _ string, onPick func(string)) {
	if want, ok := s.next(title).(string); ok {
		i := slices.IndexFunc(options, func(o app.Option) bool { return matches(o, want) })
		if i < 0 {
			s.t.Fatalf("picker %q has no option %q", title, want)
		}
		onPick(options[i].ID)
	}
}

func (s *scriptUI) MultiPick(title string, options []app.Option, selected map[string]bool, onDone func(map[string]bool)) {
	if toggles, ok := s.next(title).([]string); ok {
		out := map[string]bool{}
		maps.Copy(out, selected)
		for _, want := range toggles {
			i := slices.IndexFunc(options, func(o app.Option) bool { return matches(o, want) })
			out[options[i].ID] = !out[options[i].ID]
		}
		onDone(out)
	}
}

// Prompt submits the answer; one its Check refuses is noted and treated as a cancel.
func (s *scriptUI) Prompt(p app.Prompt, onSubmit func(string)) {
	if text, ok := s.next(p.Title).(string); ok && (text != "" || p.AllowEmpty) {
		if p.Check != nil {
			if err := p.Check(text); err != nil {
				s.notes = append(s.notes, err.Error())
				return
			}
		}
		onSubmit(text)
	}
}

func (s *scriptUI) Confirm(title, _ string, onYes func()) {
	if yes, _ := s.next(title).(bool); yes {
		onYes()
	}
}

func (s *scriptUI) Edit(title, initial string, onDone func(string)) {
	s.editorText = initial
	if text, ok := s.next(title).(string); ok {
		onDone(text)
	}
}

// Form types the answered name and presses enter, walking through required fields the way
// the real form does: each missing field's editor consumes the next answers.
func (s *scriptUI) Form(f *app.Form) {
	s.form = f
	name, ok := s.next(f.Title).(string)
	if !ok {
		return
	}
	for {
		missing, ok := f.Submit(name)
		if ok || missing < 0 {
			if f.Error != "" {
				s.notes = append(s.notes, f.Error)
			}
			return
		}
		finished := false
		f.Rows()[missing].Edit(func() { finished = true })
		if !finished {
			return // cancelled
		}
	}
}

func (s *scriptUI) Notify(_ app.Level, msg string) { s.notes = append(s.notes, msg) }
func (s *scriptUI) Focus(p app.Panel)              { s.focus = p }
func (s *scriptUI) Clipboard(text string) error    { s.clipboard = text; return nil }
func (s *scriptUI) OpenURL(string) error           { return nil }
func (s *scriptUI) Refresh()                       {}
func (s *scriptUI) ShowLatestComment()             {}

type harness struct {
	t     *testing.T
	fake  *fake.Server
	app   *app.App
	ui    *scriptUI
	async *testAsync
}

func newHarness(t *testing.T, srv *fake.Server, c *cache.Cache) *harness {
	t.Helper()
	if c == nil {
		var err error
		if c, err = cache.Open(":memory:"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
	}
	ui := &scriptUI{t: t}
	async := &testAsync{cancels: map[string]context.CancelFunc{}}
	a := app.New(srv.Client(), c, ui, async)
	a.Debounce = 0
	return &harness{t: t, fake: srv, app: a, ui: ui, async: async}
}

// do runs fn as a UI event, answering popups with answers, and waits for background work.
func (h *harness) do(fn func(*app.App), answers ...any) {
	h.t.Helper()
	h.ui.answers = append(h.ui.answers, answers...)
	h.async.UI(func() { fn(h.app) })
	h.async.wg.Wait()
	if len(h.ui.answers) > 0 {
		h.t.Fatalf("unused answers: %v", h.ui.answers)
	}
}

func (h *harness) boot() *harness {
	h.do((*app.App).Boot)
	return h
}

func (h *harness) rows() []string {
	var names []string
	for _, r := range h.app.Rows() {
		names = append(names, strings.Repeat("↳", r.Depth)+r.Task.Name)
	}
	return names
}

var backlog = app.View{Kind: "list", ID: fake.ListID, Name: "Backlog"}

func TestBootShowsMyTasksWithDetail(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	if got, want := h.rows(), []string{"Task number 1", "Task number 3", "Task number 5"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if h.app.Detail == nil || h.app.Detail.ID != "t1" || len(h.app.Comments) != 1 {
		t.Fatalf("detail = %+v, comments = %v", h.app.Detail, h.app.Comments)
	}
	if len(h.app.Hierarchy) != 1 || h.app.Hierarchy[0].Folders[0].Lists[0].ID != fake.ListID {
		t.Fatalf("hierarchy = %+v", h.app.Hierarchy)
	}
	if len(h.app.Log) == 0 {
		t.Fatal("command log is empty")
	}
}

func TestSecondStartRendersFromCacheBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, fake.Basic(5), c).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	c.Close()

	c2, _ := cache.Open(path)
	defer c2.Close()
	h2 := newHarness(t, fake.Basic(5), c2)
	h2.async.UI(func() {
		h2.app.Boot()
		// Still inside the same UI event: nothing has come back from the network yet.
		if h2.app.View.ID != fake.ListID || len(h2.app.Rows()) != 5 || h2.app.Detail == nil {
			t.Errorf("cached start: view %+v, %d rows", h2.app.View, len(h2.app.Rows()))
		}
	})
	h2.async.wg.Wait()
}

func TestFilter(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.SetFilter("number 3") })
	if got := h.rows(); !slices.Equal(got, []string{"Task number 3"}) {
		t.Fatalf("rows = %v", got)
	}
	if h.app.SelectedID != "t3" {
		t.Fatalf("selection didn't follow the filter: %q", h.app.SelectedID)
	}
}

func TestSetStatusIsOptimisticAndSelectionFollowsTheTask(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SetStatus, "progress")
	if got := h.fake.Task("t1").Status.Status; got != "in progress" {
		t.Fatalf("server status = %q", got)
	}
	if rows := h.rows(); rows[len(rows)-1] != "Task number 1" {
		t.Fatalf("in-progress task should sort last: %v", rows)
	}
	if h.app.SelectedID != "t1" || h.app.Detail.ID != "t1" {
		t.Fatalf("selection moved to %q", h.app.SelectedID)
	}
}

func TestNextStatusPreselectsTheFollowingStatus(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).NextStatus, "enter") // to do → in progress
	if h.ui.highlighted != 1 || h.fake.Task("t1").Status.Status != "in progress" {
		t.Fatalf("highlighted %d, status %q", h.ui.highlighted, h.fake.Task("t1").Status.Status)
	}
	h.do((*app.App).NextStatus, "enter") // in progress → complete
	if got := h.fake.Task("t1").Status.Status; got != "complete" {
		t.Fatalf("status %q", got)
	}
	h.do((*app.App).NextStatus, '1') // jump back with a number key
	if got := h.fake.Task("t1").Status.Status; got != "to do" {
		t.Fatalf("status %q", got)
	}
}

func TestFailedUpdateReverts(t *testing.T) {
	srv := fake.Basic(5)
	srv.FailUpdates = true
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).Rename, "Renamed")
	if h.app.Detail.Name != "Task number 1" || h.rows()[0] != "Task number 1" {
		t.Fatalf("not reverted: %q", h.app.Detail.Name)
	}
	if len(h.ui.notes) == 0 || !strings.Contains(h.ui.notes[0], "reverted") {
		t.Fatalf("notes = %v", h.ui.notes)
	}
}

func TestPriorityDueAndDescription(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SetPriority, '1')
	if p := h.fake.Task("t1").Priority; p == nil || p.Priority != "urgent" {
		t.Fatalf("priority = %+v", p)
	}
	h.do((*app.App).SetDue, "2030-01-15")
	if got := render.DueCell(new(h.fake.Task("t1")), time.Now()).Text; got != "Jan 2030" {
		t.Fatalf("due = %q", got)
	}
	h.do((*app.App).EditDescription, true, "New *body*") // yes, edit
	if got := h.fake.Task("t1").MarkdownDescription; got != "New *body*" {
		t.Fatalf("server description = %q", got)
	}
	if got := h.app.Detail.MarkdownDescription; got != "New *body*" {
		t.Fatalf("local description = %q (update responses carry no markdown)", got)
	}
}

func TestCreateSubtaskCommentDelete(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.NewTask(false) }, "Brand new", '2') // Severity is required: pick S2
	if h.app.Detail.Name != "Brand new" || h.app.Detail.Pending {
		t.Fatalf("detail = %+v", h.app.Detail)
	}
	parent := h.app.Detail.ID

	h.do(func(a *app.App) { a.NewTask(true) }, "Child", '1')
	if got := h.fake.Task(h.app.Detail.ID).Parent; string(got) != parent {
		t.Fatalf("parent = %q, want %q", got, parent)
	}
	if !slices.Contains(h.rows(), "↳Child") {
		t.Fatalf("subtask not nested: %v", h.rows())
	}

	child := h.app.Detail.ID
	h.do((*app.App).Comment, "hello")
	if len(h.fake.Comments[child]) != 1 || len(h.app.Comments) != 1 || h.app.Comments[0].Pending {
		t.Fatalf("comments = %+v", h.app.Comments)
	}

	h.do((*app.App).Delete, true)
	if _, ok := h.fake.Tasks[child]; ok || slices.Contains(h.rows(), "↳Child") {
		t.Fatal("task not deleted")
	}
}

func TestGoToCustomIDOutsideView(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).GoTo, "https://app.clickup.com/t/T1/DEV-2")
	if h.app.Detail.ID != "t2" || h.ui.focus != app.PanelDetail {
		t.Fatalf("detail = %v, focus = %v", h.app.Detail.ID, h.ui.focus)
	}
}

func TestToggleClosedAndAssignMe(t *testing.T) {
	srv := fake.Basic(5)
	srv.Tasks["t3"].Status = fake.Statuses[2]
	h := newHarness(t, srv, nil).boot()
	if n := len(h.rows()); n != 2 {
		t.Fatalf("%d rows", n)
	}
	h.do((*app.App).ToggleClosed)
	if n := len(h.rows()); n != 3 {
		t.Fatalf("%d rows with closed", n)
	}
	h.do((*app.App).AssignMe) // t1 is assigned to me: unassign
	if a := h.fake.Task("t1").Assignees; len(a) != 0 {
		t.Fatalf("assignees = %v", a)
	}
}

func TestAssignByName(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).Assign, []string{"ali"})
	if got := usernames(h.fake.Task("t1").Assignees); !slices.Equal(got, []string{"tester", "alice"}) {
		t.Fatalf("assignees = %v", got)
	}
	h.do((*app.App).Assign, []string{"test"})
	if got := usernames(h.fake.Task("t1").Assignees); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("assignees = %v", got)
	}
}

func usernames(users []clickup.User) []string {
	var out []string
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func TestCustomFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	field := func(name string) clickup.CustomField { return h.fake.Field("t1", name) }

	// Menu hotkeys come from the field names: Areas a, Customer facing c, Estimate e,
	// Reviewer r, Score s, Severity v (s is taken).
	h.do((*app.App).EditField, 'v', '2')
	if got := string(field("Severity").Value); got != "1" {
		t.Fatalf("severity = %s, want orderindex 1", got)
	}
	if f, _ := h.app.Detail.Field("f-sev"); strings.TrimSpace(style.Strip(render.FieldText(f))) != "S2" {
		t.Fatalf("local severity = %s", render.FieldText(f))
	}

	h.do((*app.App).EditField, 'a', []string{"frontend", "backend"})
	if got := string(field("Areas").Value); got != `["l1","l2"]` {
		t.Fatalf("areas = %s", got)
	}

	h.do((*app.App).EditField, 'c')
	if got := string(field("Customer facing").Value); got != "true" {
		t.Fatalf("checkbox = %s", got)
	}

	h.do((*app.App).EditField, 'e', "3.5")
	h.do((*app.App).EditField, 'e', nil) // cancelling keeps the value
	if got := string(field("Estimate").Value); got != "3.5" {
		t.Fatalf("estimate = %s", got)
	}
	h.do((*app.App).EditField, 'e', "") // an empty answer clears it
	if f := field("Estimate"); f.IsSet() {
		t.Fatalf("estimate not cleared: %s", f.Value)
	}

	h.do((*app.App).EditField, 's') // formula: read-only
	if !strings.Contains(h.ui.notes[len(h.ui.notes)-1], "can't be edited") {
		t.Fatalf("notes = %v", h.ui.notes)
	}
}

func TestUsersFieldAndFailedFieldUpdateReverts(t *testing.T) {
	srv := fake.Basic(5)
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).EditField, 'r', []string{"alice"})
	if got := usernames(render.UserValues(new(srv.Field("t1", "Reviewer")))); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("reviewer = %v", got)
	}

	srv.FailUpdates = true
	h.do((*app.App).EditField, 'c')
	if f, _ := h.app.Detail.Field("f-cust"); f.IsSet() {
		t.Fatalf("checkbox not reverted: %s", f.Value)
	}
}

func TestTabsAndJumpToList(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).SwitchTab)
	if h.ui.focus != app.PanelLists {
		t.Fatal("with no list opened yet, switching tabs should point at the lists panel")
	}
	h.do((*app.App).JumpToList, "Product / Backlog")
	if h.app.View.ID != fake.ListID || len(h.rows()) != 5 {
		t.Fatalf("view = %+v", h.app.View)
	}
	h.do((*app.App).SwitchTab)
	if h.app.View.Kind != "my" {
		t.Fatalf("view = %+v", h.app.View)
	}
	h.do((*app.App).SwitchTab)
	if h.app.View.ID != fake.ListID {
		t.Fatalf("view = %+v", h.app.View)
	}
}

func TestCopyMenu(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).CopyMenu, 'm')
	if want := "[Task number 1](https://app.clickup.com/t/t1)"; h.ui.clipboard != want {
		t.Fatalf("clipboard = %q", h.ui.clipboard)
	}
	h.do((*app.App).CopyMenu, 'd') // the whole description, as markdown
	if want := "Description of **task 1**"; h.ui.clipboard != want {
		t.Fatalf("clipboard = %q", h.ui.clipboard)
	}
}

// Scrolling through tasks only fetches details for the one you stop on.
func TestDetailFetchIsDebounced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := fake.Basic(5)
		h := newHarness(t, srv, nil)
		h.app.Debounce = 150 * time.Millisecond
		h.boot()
		h.do(func(a *app.App) {
			a.Select(1)
			a.Select(2)
		})
		if n := srv.Count("GET /api/v2/task/t3"); n != 0 {
			t.Fatalf("fetched a task that was only scrolled past (%d times)", n)
		}
		if n := srv.Count("GET /api/v2/task/t5"); n != 1 {
			t.Fatalf("fetched the final task %d times", n)
		}
	})
}

// The create form lists the list's fields with required ones first, and won't create the
// task until they are set; optional properties go into the same request.
func TestNewTaskFormRequiresRequiredFields(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.NewTask(false) }, "Needs severity", nil) // cancel the Severity menu
	if h.ui.form == nil || len(h.fake.Tasks) != 5 {
		t.Fatalf("task created without its required field (%d tasks)", len(h.fake.Tasks))
	}
	var labels []string
	for _, r := range h.ui.form.Rows() {
		labels = append(labels, r.Label)
	}
	if want := "Status Assignees Priority Due date Severity Areas Customer facing Estimate Reviewer"; strings.Join(labels, " ") != want {
		t.Fatalf("rows = %v", labels)
	}
	if rows := h.ui.form.Rows(); !rows[4].Required || !rows[4].Missing {
		t.Fatalf("severity row = %+v", rows[4])
	}

	// Set optional properties first, then create: enter asks for the required Severity.
	form := h.ui.form
	h.do(func(*app.App) {
		rows := form.Rows()
		rows[2].Edit(func() {}) // Priority
		rows[1].Edit(func() {}) // Assignees
		rows[5].Edit(func() {}) // Areas
	}, '2', []string{"alice"}, []string{"backend"})
	h.do(func(*app.App) {
		missing, ok := form.Submit("Hotfix")
		if ok || missing != 4 {
			t.Errorf("submit = %d, %v", missing, ok)
		}
		form.Rows()[missing].Edit(func() {})
		if _, ok := form.Submit("Hotfix"); !ok {
			t.Error("submit failed with all required fields set")
		}
	}, '1')
	created := h.app.Detail
	server := h.fake.Task(created.ID)
	if server.Name != "Hotfix" || server.Priority == nil || server.Priority.Priority != "high" {
		t.Fatalf("server task = %+v", server)
	}
	if got := usernames(server.Assignees); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("assignees = %v", got)
	}
	if got := string(h.fake.Field(created.ID, "Severity").Value); got != "0" {
		t.Fatalf("severity = %s, want S1 (orderindex 0)", got)
	}
	if got := string(h.fake.Field(created.ID, "Areas").Value); got != `["l2"]` {
		t.Fatalf("areas = %s", got)
	}
}

// Pinned tasks survive restarts: they render from the cache at once and refresh after.
// A pinned task that was deleted in ClickUp gets unpinned.
func TestPinnedTasksPersistAcrossStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c, _ := cache.Open(path)
	h := newHarness(t, fake.Basic(5), c).boot()
	h.do((*app.App).TogglePin) // t1 from Mine
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.Select(1) }) // t2, not in Mine
	h.do((*app.App).TogglePin)
	c.Close()

	srv := fake.Basic(5)
	srv.Tasks["t2"].Name = "Renamed elsewhere"
	srv.Remove("t1")
	c2, _ := cache.Open(path)
	defer c2.Close()
	h2 := newHarness(t, srv, c2)
	h2.async.UI(func() {
		h2.app.Boot()
		if len(h2.app.Pinned) != 2 || h2.app.Pinned[1].Name != "Task number 2" {
			t.Errorf("pinned from cache = %v", h2.app.Pinned)
		}
	})
	h2.async.wg.Wait()
	if len(h2.app.Pinned) != 1 || h2.app.Pinned[0].Name != "Renamed elsewhere" {
		t.Fatalf("pinned after refresh = %+v", h2.app.Pinned)
	}
}

// wednesday is the fixed "now" of the time tracking tests: Wednesday 23 September 2026 15:00.
var wednesday = time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local)

func at(day int, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, time.Local)
}

func durations(entries []clickup.TimeEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, (time.Duration(e.Duration.Int()) * time.Millisecond).String())
	}
	return out
}

func timeHarness(t *testing.T) *harness {
	srv := fake.Basic(5)
	srv.Entries = []clickup.TimeEntry{
		srv.Entry("t1", at(21, 9, 0), 2*time.Hour, "morning"),
		srv.Entry("t1", at(21, 14, 0), time.Hour, "afternoon"),
		srv.Entry("t3", at(22, 10, 0), 30*time.Minute, ""),
		srv.Entry("t3", at(14, 10, 0), time.Hour, "last week"),
	}
	srv.Now = func() time.Time { return wednesday } // the same clock as the app, whatever today is
	h := newHarness(t, srv, nil)
	h.app.Now = func() time.Time { return wednesday }
	return h.boot()
}

func TestLogTimeOnTask(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).LogTime, "1h30 yesterday fixed login")
	e := h.fake.Entries[len(h.fake.Entries)-1]
	if e.TaskID() != "t1" || e.Description != "fixed login" || !e.StartTime().Equal(at(22, 9, 0)) ||
		time.Duration(e.Duration.Int())*time.Millisecond != 90*time.Minute {
		t.Fatalf("entry = %+v", e)
	}
	h.do((*app.App).LogTime, "45m") // no day: it ends now
	e = h.fake.Entries[len(h.fake.Entries)-1]
	if !e.StartTime().Equal(wednesday.Add(-45 * time.Minute)) {
		t.Fatalf("start = %v", e.StartTime())
	}
}

func TestTimesheetGridAndEditing(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	rows, days, total := h.app.SheetRows()
	if len(rows) != 2 || rows[0].TaskID != "t1" || rows[0].Days[0] != 3*time.Hour || rows[1].Days[1] != 30*time.Minute {
		t.Fatalf("rows = %+v", rows)
	}
	if days[0] != 3*time.Hour || total != 3*time.Hour+30*time.Minute || h.app.Sheet.Col != 2 {
		t.Fatalf("days = %v, total = %v, today's column = %d", days, total, h.app.Sheet.Col)
	}

	// enter adds a new entry, also where the day has time: the form starts it right after
	// the day's last entry, so typing the duration is enough.
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 1, 1; a.AddToCell() }, "0:45")
	if rows, _, _ := h.app.SheetRows(); rows[1].Days[1] != 75*time.Minute || len(rows[1].Entries[1]) != 2 {
		t.Fatalf("Tue for t3 = %v in %d entries", rows[1].Days[1], len(rows[1].Entries[1]))
	}
	if e := h.fake.Entries[len(h.fake.Entries)-1]; !e.StartTime().Equal(at(22, 10, 30)) || e.TaskID() != "t3" {
		t.Fatalf("new entry = %+v", e)
	}

	// The form's fields: start, end (setting it recalculates the duration) and note.
	h.do(func(a *app.App) { a.AddToCell() }, nil) // open the form without saving yet
	form := h.ui.form
	if end := style.Strip(form.Rows()[2].Value); !strings.Contains(end, "type a duration") {
		t.Fatalf("end without a duration = %q", end)
	}
	h.do(func(*app.App) { form.Rows()[1].Edit(func() {}) }, "15:00") // Start
	h.do(func(*app.App) { form.Rows()[2].Edit(func() {}) }, "16:30") // End
	h.do(func(*app.App) { form.Rows()[3].Edit(func() {}) }, "retro") // Note
	if end := style.Strip(form.Rows()[2].Value); form.Name != "1:30" || !strings.HasPrefix(end, "16:30") {
		t.Fatalf("duration %q, end %q", form.Name, end)
	}
	h.do(func(*app.App) { form.Submit(form.Name) })
	if e := h.fake.Entries[len(h.fake.Entries)-1]; !e.StartTime().Equal(at(22, 15, 0)) || e.Description != "retro" ||
		time.Duration(e.Duration.Int())*time.Millisecond != 90*time.Minute {
		t.Fatalf("entry from the form = %+v", e)
	}

	// e on a day with several entries: pick one, then edit it in the same form…
	h.do(func(a *app.App) { a.EditCellEntry() }, '1', 'e', "0:40")
	if got := durations(h.fake.Entries); got[2] != "40m0s" {
		t.Fatalf("durations = %v", got)
	}
	// …or delete it.
	h.do(func(a *app.App) { a.EditCellEntry() }, '3', 'd', true)
	if slices.ContainsFunc(h.fake.Entries, func(e clickup.TimeEntry) bool { return e.Description == "retro" }) {
		t.Fatal("entry not deleted")
	}
	// Moving an entry to another day keeps its start time.
	h.do(func(a *app.App) { a.EditCellEntry() }, '1', 'e', nil)
	form = h.ui.form
	h.do(func(*app.App) { form.Rows()[0].Edit(func() {}) }, "wed")
	h.do(func(*app.App) { form.Submit(form.Name) })
	if e := h.fake.Entries[2]; !e.StartTime().Equal(at(23, 10, 0)) {
		t.Fatalf("moved entry starts %v", e.StartTime())
	}
	// e on an empty cell explains instead.
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 1, 4; a.EditCellEntry() })
	if !strings.Contains(h.ui.notes[len(h.ui.notes)-1], "enter adds") {
		t.Fatalf("notes = %v", h.ui.notes)
	}
	// Clear a cell (confirmed).
	h.do(func(a *app.App) { a.Sheet.Row, a.Sheet.Col = 0, 0; a.ClearCell() }, true)
	if slices.ContainsFunc(h.fake.Entries, func(e clickup.TimeEntry) bool { return e.TaskID() == "t1" && e.StartTime().Day() == 21 }) {
		t.Fatal("cell not cleared")
	}

	h.do(func(a *app.App) { a.ShiftWeek(-1) })
	if rows, _, total := h.app.SheetRows(); len(rows) != 1 || total != time.Hour {
		t.Fatalf("last week = %+v", rows)
	}
	h.do(func(a *app.App) { a.ShiftWeek(0) })
	if !h.app.Sheet.Week.Equal(at(21, 0, 0)) {
		t.Fatalf("this week = %v", h.app.Sheet.Week)
	}
}

func TestTimesheetAddTaskRowAndFailedLogReverts(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	h.do((*app.App).AddSheetTask, "Task number 5")
	rows, _, _ := h.app.SheetRows()
	if len(rows) != 3 || rows[h.app.Sheet.Row].TaskID != "t5" {
		t.Fatalf("rows = %+v, selected %d", rows, h.app.Sheet.Row)
	}
	h.fake.FailUpdates = true
	h.do((*app.App).AddToCell, "2h")
	if _, _, total := h.app.SheetRows(); total != 3*time.Hour+30*time.Minute {
		t.Fatalf("failed log wasn't reverted: total %v", total)
	}
}

func TestTimerAndTaskTimeMenu(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).ToggleTimer)
	if h.fake.Running == nil || h.app.Timer == nil || !h.app.TimerRunning() || !strings.Contains(style.Strip(h.app.TimerLine()), "DEV-1") {
		t.Fatalf("timer not running: %+v", h.app.Timer)
	}
	h.do((*app.App).ToggleTimer)
	if h.fake.Running != nil || h.app.Timer != nil {
		t.Fatal("timer not stopped")
	}
	if last := h.fake.Entries[len(h.fake.Entries)-1]; last.TaskID() != "t1" || last.Running() {
		t.Fatalf("stopped timer entry = %+v", last)
	}

	// w lists the task's entries, newest first (the stopped timer, afternoon, morning);
	// edit the "morning" one: the form keeps its start and note.
	h.do((*app.App).TaskTime, '3', 'e', "3:00")
	morning := h.fake.Entries[0]
	if morning.Description != "morning" || !morning.StartTime().Equal(at(21, 9, 0)) ||
		time.Duration(morning.Duration.Int())*time.Millisecond != 3*time.Hour {
		t.Fatalf("edited entry = %+v", morning)
	}
}

// Two edits of one task in flight: the first fails, the second succeeds. Only the first
// edit's field is reverted.
func TestFailedEditDoesNotUndoAnotherEdit(t *testing.T) {
	srv := fake.Basic(5)
	srv.FailField = "status"
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).SetStatus, nil) // load the list's statuses, so the next picker opens at once
	h.do(func(a *app.App) {
		a.SetStatus()   // snapshot taken here, before the priority change…
		a.SetPriority() // …which is in flight at the same time
	}, "progress", '1')
	if s := h.app.Detail.Status.Status; s != "to do" {
		t.Fatalf("failed status change not reverted: %q", s)
	}
	if p := h.app.Detail.Priority; p == nil || p.Priority != "urgent" {
		t.Fatalf("the successful priority change was undone: %+v", p)
	}
	if p := srv.Task("t1").Priority; p == nil || p.Priority != "urgent" {
		t.Fatalf("server priority = %+v", p)
	}
}

// Deleting a task from the pinned panel removes it from the list too.
func TestDeletePinnedTaskRemovesListCopy(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).TogglePin)
	h.do(func(a *app.App) { a.SelectPinned(0) })
	if !slices.Contains(h.app.Pinned, h.app.Detail) {
		t.Fatal("detail should be the pinned copy")
	}
	h.do((*app.App).Delete, true)
	if slices.Contains(h.rows(), "Task number 1") || len(h.app.Pinned) != 0 {
		t.Fatalf("rows = %v, pinned = %d", h.rows(), len(h.app.Pinned))
	}
	if h.app.Detail != nil && h.app.Detail.ID == "t1" {
		t.Fatal("the deleted task is still shown")
	}
}

// Choosing people twice in the create form keeps everyone chosen.
func TestNewTaskFormUsersFieldKeepsEveryone(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.NewTask(false) }, nil)
	form := h.ui.form
	reviewer := slices.IndexFunc(form.Rows(), func(r app.FormRow) bool { return r.Label == "Reviewer" })
	severity := slices.IndexFunc(form.Rows(), func(r app.FormRow) bool { return r.Label == "Severity" })
	h.do(func(*app.App) { form.Rows()[reviewer].Edit(func() {}) }, []string{"alice"})
	h.do(func(*app.App) { form.Rows()[reviewer].Edit(func() {}) }, []string{"tester"})
	h.do(func(*app.App) { form.Rows()[severity].Edit(func() {}) }, '1')
	h.do(func(*app.App) { form.Submit("Two reviewers") })
	got := usernames(render.UserValues(new(h.fake.Field(h.app.Detail.ID, "Reviewer"))))
	slices.Sort(got)
	if !slices.Equal(got, []string{"alice", "tester"}) {
		t.Fatalf("reviewers = %v", got)
	}
}

// Nothing of one workspace lingers in another.
func TestWorkspaceSwitchResetsState(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	h.async.UI(func() {
		h.app.SetFilter("number")
		h.app.EnterWorkspace("other")
		// Checked in the same UI event: nothing has loaded for the new workspace yet.
		if h.app.Filter != "" || !h.app.Sheet.Week.IsZero() || h.app.Timer != nil || h.app.Detail != nil {
			t.Errorf("filter %q, sheet week %v, timer %v, detail %v", h.app.Filter, h.app.Sheet.Week, h.app.Timer, h.app.Detail)
		}
	})
	h.async.wg.Wait()
}

func TestGoToEmptyReference(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).GoTo, "https://app.clickup.com/t/")
	if !strings.Contains(h.ui.notes[len(h.ui.notes)-1], "doesn't point at a task") || h.app.Detail.ID != "t1" {
		t.Fatalf("notes = %v, detail %s", h.ui.notes, h.app.Detail.ID)
	}
}

// On the last status, space, enter keeps the task where it is instead of reopening it.
func TestNextStatusDoesNotWrapAround(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).NextStatus, '3') // complete
	h.do((*app.App).NextStatus, "enter")
	if got := h.fake.Task("t1").Status.Status; got != "complete" || h.ui.highlighted != 2 {
		t.Fatalf("status %q, highlighted %d", got, h.ui.highlighted)
	}
}

// A filter that hides every task also hides it from actions.
func TestFilterHidingEverythingClearsTheTask(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.SetFilter("no such task") })
	if h.app.Detail != nil || h.app.SelectedID != "" {
		t.Fatalf("detail %v, selected %q", h.app.Detail, h.app.SelectedID)
	}
	h.do((*app.App).NextStatus)
	if note := h.ui.notes[len(h.ui.notes)-1]; note != "No task selected" {
		t.Fatalf("note = %q", note)
	}
	h.do(func(a *app.App) { a.SetFilter("") })
	if h.app.Detail == nil || h.app.Detail.ID != "t1" {
		t.Fatalf("clearing the filter should select again: %v", h.app.Detail)
	}
}

// a says which way it toggled.
func TestAssignMeSaysWhatHappened(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).AssignMe)
	if note := h.ui.notes[len(h.ui.notes)-1]; note != "Unassigned you from DEV-1" {
		t.Fatalf("note = %q", note)
	}
	h.do((*app.App).AssignMe)
	if note := h.ui.notes[len(h.ui.notes)-1]; note != "Assigned you to DEV-1" {
		t.Fatalf("note = %q", note)
	}
}

// L logs through the entry form; its input takes the one-line form too.
func TestLogTimeFormTakesOneLine(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).LogTime, "1h 13:00 yesterday pairing")
	e := h.fake.Entries[len(h.fake.Entries)-1]
	yesterday := time.Date(2026, 9, 22, 13, 0, 0, 0, time.Local)
	if !e.StartTime().Equal(yesterday) || e.Description != "pairing" || e.Duration.Int() != time.Hour.Milliseconds() {
		t.Fatalf("entry %v %q %s", e.StartTime(), e.Description, e.Duration)
	}
	h.do((*app.App).LogTime, "tomorrow")
	if note := h.ui.notes[len(h.ui.notes)-1]; !strings.Contains(note, "start with a duration") {
		t.Fatalf("note = %q", note)
	}
}

// Rows added to the timesheet stay with their week; tasks outside the offered ones can be
// added by id.
func TestTimesheetRowsByIDStayWithTheirWeek(t *testing.T) {
	h := timeHarness(t)
	h.do((*app.App).OpenTimesheet)
	h.do((*app.App).AddSheetTask, "another task", "DEV-4")
	rows, _, _ := h.app.SheetRows()
	if rows[h.app.Sheet.Row].TaskID != "t4" {
		t.Fatalf("rows = %+v", rows)
	}
	n := len(rows)
	h.do(func(a *app.App) { a.ShiftWeek(1) })
	if rows, _, _ := h.app.SheetRows(); len(rows) != 0 {
		t.Fatalf("next week has rows: %+v", rows)
	}
	h.do(func(a *app.App) { a.ShiftWeek(-1) })
	if rows, _, _ := h.app.SheetRows(); len(rows) != n {
		t.Fatalf("back on the week: %d rows, want %d", len(rows), n)
	}
}

func TestSortBy(t *testing.T) {
	srv := fake.Basic(5)
	srv.Tasks["t3"].Priority = &clickup.Priority{ID: "1", Priority: "urgent"}
	srv.Tasks["t5"].Priority = &clickup.Priority{ID: "3", Priority: "normal"}
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).SortMenu, '3') // priority
	if got, want := h.rows(), []string{"Task number 3", "Task number 5", "Task number 1"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if g := h.app.Rows()[2].Group; style.Strip(g.Label) != "No priority" {
		t.Fatalf("group = %+v", g)
	}
	// Remembered for the next start.
	if h2 := newHarness(t, srv, h.app.Cache); h2.app.Group != render.ByPriority {
		t.Fatalf("group after restart = %q", h2.app.Group)
	}
}

// m moves a task to another list: it leaves the list at once, and comes back if that fails.
func TestMoveTask(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.fake.FailUpdates = true
	h.do((*app.App).MoveTask, "Inbox")
	if len(h.rows()) != 5 || h.fake.Task("t1").List.ID != fake.ListID {
		t.Fatalf("failed move: rows %v, list %s", h.rows(), h.fake.Task("t1").List.ID)
	}
	h.fake.FailUpdates = false
	h.do((*app.App).MoveTask, "Inbox")
	if got := h.fake.Task("t1").List.ID; got != "L2" {
		t.Fatalf("server list = %s", got)
	}
	if slices.Contains(h.rows(), "Task number 1") || h.app.Detail.ID == "t1" {
		t.Fatalf("moved task still in the list: %v, detail %s", h.rows(), h.app.Detail.ID)
	}
	h.do(func(a *app.App) { a.OpenView(app.View{Kind: "list", ID: "L2", Name: "Inbox"}) })
	if got := h.rows(); !slices.Equal(got, []string{"Task number 1"}) {
		t.Fatalf("Inbox rows = %v", got)
	}
}

// Comments tag the members they @mention.
func TestCommentMentions(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do((*app.App).Comment, "@alice can you look?")
	if len(h.fake.Mentions) != 1 || h.fake.Mentions[0].User.ID != fake.Other.ID {
		t.Fatalf("mentions = %+v", h.fake.Mentions)
	}
	// ClickUp's comment_text puts mentions last; the comment reads as written anyway.
	if c := h.app.Comments[len(h.app.Comments)-1]; c.Text() != "@alice can you look?" || c.CommentText != " can you look?@alice" {
		t.Fatalf("comment = %q (comment_text %q)", c.Text(), c.CommentText)
	}
	if note := h.ui.notes[len(h.ui.notes)-1]; note != "Comment posted on DEV-1" {
		t.Fatalf("note = %q", note)
	}
}

// A task fetched on its own (the task panel, pinned refreshes) reports another orderindex than
// its list. The list keeps its own order: rows don't jump when you select one.
func TestSelectingDoesNotReorder(t *testing.T) {
	h := newHarness(t, fake.Basic(5), nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	before := h.rows()
	for i := range 5 {
		h.do(func(a *app.App) { a.Select(i) })
		if got := h.rows(); !slices.Equal(got, before) {
			t.Fatalf("selecting row %d reordered the list: %v", i, got)
		}
	}
	h.do((*app.App).TogglePin)
	h.do((*app.App).RefreshPinned)
	if got := h.rows(); !slices.Equal(got, before) {
		t.Fatalf("refreshing pinned tasks reordered the list: %v", got)
	}
}

// A task whose home is another list, added to this one too ("tasks in multiple lists"), shows
// in this list.
func TestTaskFromAnotherListShows(t *testing.T) {
	srv := fake.Basic(3)
	srv.Tasks["t3"].List = clickup.Ref{ID: "L2", Name: "Inbox"}
	srv.Tasks["t3"].Locations = []clickup.Ref{{ID: fake.ListID, Name: "Backlog"}}
	h := newHarness(t, srv, nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	if got := h.rows(); !slices.Contains(got, "Task number 3") {
		t.Fatalf("Backlog rows = %v, want the task from Inbox too", got)
	}
	h.do(func(a *app.App) { a.OpenView(app.View{Kind: "list", ID: "L2", Name: "Inbox"}) })
	if got := h.rows(); !slices.Equal(got, []string{"Task number 3"}) {
		t.Fatalf("Inbox rows = %v", got)
	}
}

// The tracked time comes only with a task fetched on its own; a list refresh or an edit made
// through another copy must not wipe it from the task panel.
func TestTrackedTimeSurvivesRefreshAndEdits(t *testing.T) {
	srv := fake.Basic(5)
	srv.Tasks["t1"].TimeSpent = "5400000" // 1:30
	h := newHarness(t, srv, nil).boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.Select(0) })
	tracked := func() string {
		return strings.Join(strings.Fields(style.Strip(render.Detail(h.app.Detail, nil, time.Now(), 80))), " ")
	}
	if !strings.Contains(tracked(), "tracked 1:30") {
		t.Fatalf("task panel: %s", tracked())
	}
	srv.Tasks["t2"].Name = "Changed elsewhere" // so the refresh brings new list data
	h.do(func(a *app.App) { a.LoadTasks(true) })
	if !strings.Contains(tracked(), "tracked 1:30") {
		t.Fatalf("after a list refresh: %s", tracked())
	}
	h.do((*app.App).TogglePin)
	h.do((*app.App).Rename, "Renamed") // edits the list copy, which updates the pinned one
	if p := h.app.Pinned[0]; p.TimeSpent != "5400000" || p.Name != "Renamed" {
		t.Fatalf("pinned copy: name %q, tracked %q", p.Name, p.TimeSpent)
	}
}

// Replies in a comment thread come from their own endpoint; the task panel shows them under
// their comment.
func TestCommentThreadReplies(t *testing.T) {
	srv := fake.Basic(5)
	srv.Replies["c1"] = []clickup.Comment{
		{ID: "r1", CommentText: "a reply in the thread", User: fake.Other, Date: "1700000100000"},
	}
	h := newHarness(t, srv, nil).boot()
	c := h.app.Comments
	if len(c) != 1 || len(c[0].Replies) != 1 || c[0].Replies[0].CommentText != "a reply in the thread" {
		t.Fatalf("comments = %+v", c)
	}
	panel := style.Strip(render.Detail(h.app.Detail, h.app.Comments, time.Now(), 80))
	for _, want := range []string{"Comments (2)", "first!", "↳ alice", "    a reply in the thread"} {
		if !strings.Contains(panel, want) {
			t.Errorf("task panel lacks %q:\n%s", want, panel)
		}
	}
}

// Stepping through the list at a normal pace (a key press every 300ms) fetches nothing until
// you stop: then only the task you stopped on.
func TestSteppingThroughTasksFetchesOnlyWhereYouStop(t *testing.T) {
	c, _ := cache.Open(":memory:")
	defer c.Close()
	if d := app.New(fake.Basic(1).Client(), c, &scriptUI{t: t}, &testAsync{}).Debounce; d < 400*time.Millisecond {
		t.Fatalf("the default wait is %v, shorter than the gap between key presses", d)
	}
	synctest.Test(t, func(t *testing.T) {
		srv := fake.Basic(10)
		h := newHarness(t, srv, nil)
		h.boot()
		h.do(func(a *app.App) { a.OpenView(backlog) })
		h.app.Debounce = 400 * time.Millisecond
		before := len(srv.Requests)
		for i := range 10 {
			h.async.UI(func() { h.app.Select(i) })
			time.Sleep(300 * time.Millisecond)
		}
		h.async.wg.Wait()
		fetched := srv.Requests[before:]
		if want := []string{"GET /api/v2/task/t10", "GET /api/v2/task/t10/comment"}; !slices.Equal(slices.Sorted(slices.Values(fetched)), want) {
			t.Fatalf("requests = %v, want only the last task: %v", fetched, want)
		}
	})
}

// Pinned tasks in the task list come along with its refresh; the others are fetched every
// five minutes, not every minute.
func TestPinnedRefreshIsFrugal(t *testing.T) {
	srv := fake.Basic(5)
	h := newHarness(t, srv, nil)
	clock := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	h.app.Now = func() time.Time { return clock }
	h.boot()
	h.do(func(a *app.App) { a.OpenView(backlog) })
	h.do(func(a *app.App) { a.Select(0) })
	h.do((*app.App).TogglePin)         // t1: in the list
	clock = clock.Add(5 * time.Minute) // the fetch at start is five minutes ago
	h.do(func(a *app.App) { a.OpenView(app.View{Kind: "list", ID: "L2", Name: "Inbox"}) })
	h.do(func(a *app.App) { a.OpenView(backlog) })
	srv.Tasks["t5"].List = clickup.Ref{ID: "L2", Name: "Inbox"} // t5 lives elsewhere now…
	h.do(func(a *app.App) { a.Select(4) })
	h.do((*app.App).TogglePin) // …and is pinned too

	srv.Tasks["t1"].Name = "Renamed in ClickUp"
	srv.Tasks["t1"].DateUpdated = "1800000000000"
	count := func() (t1, t5 int) { return srv.Count("GET /api/v2/task/t1"), srv.Count("GET /api/v2/task/t5") }
	t1, t5 := count()
	h.do((*app.App).AutoRefresh)
	if n1, _ := count(); n1 != t1 {
		t.Error("fetched the pinned task that's in the list on its own")
	}
	if p := h.app.Pinned[0]; p.Name != "Renamed in ClickUp" {
		t.Errorf("pinned copy didn't follow the list: %q", p.Name)
	}
	if _, n5 := count(); n5 != t5+1 {
		t.Fatalf("the pinned task outside the list: %d fetches, want 1", n5-t5)
	}
	for range 4 { // the next minutes: nothing for it
		clock = clock.Add(time.Minute)
		h.do((*app.App).AutoRefresh)
	}
	if _, n5 := count(); n5 != t5+1 {
		t.Errorf("fetched again within five minutes: %d", n5-t5)
	}
	clock = clock.Add(time.Minute)
	h.do((*app.App).AutoRefresh)
	if _, n5 := count(); n5 != t5+2 {
		t.Errorf("not fetched after five minutes: %d", n5-t5)
	}
}

// Saving the description unchanged sends nothing: not even when the editor wrote Windows line
// endings or a byte order mark. (A save rebuilds the description from markdown, which would drop
// text colours.)
func TestUnchangedDescriptionIsNotSaved(t *testing.T) {
	srv := fake.Basic(3)
	srv.Tasks["t1"].MarkdownDescription = "Line one\n\n- a point\n"
	h := newHarness(t, srv, nil).boot()
	puts := func() int { return srv.Count("PUT /api/v2/task/t1") }
	for _, saved := range []string{
		"Line one\n\n- a point\n",
		"Line one\n\n- a point",               // an editor that drops the last newline
		"Line one\r\n\r\n- a point\r\n",       // Notepad's line endings
		"\ufeffLine one\r\n\r\n- a point\r\n", // and its byte order mark
	} {
		h.do((*app.App).EditDescription, true, saved)
		if puts() != 0 {
			t.Fatalf("saving %q unchanged sent an update", saved)
		}
	}
	h.do((*app.App).EditDescription, true, "Line one\r\n\r\n- a new point\r\n")
	if puts() != 1 || srv.Task("t1").MarkdownDescription != "Line one\n\n- a new point\n" {
		t.Fatalf("%d updates, description %q", puts(), srv.Task("t1").MarkdownDescription)
	}
}

// e warns before the editor opens: saving drops ClickUp-only formatting. No means no editor.
func TestDescriptionWarnsBeforeEditing(t *testing.T) {
	srv := fake.Basic(3)
	h := newHarness(t, srv, nil).boot()
	h.do((*app.App).EditDescription, false) // no: the editor doesn't open (it would ask for an answer)
	if h.ui.titles[len(h.ui.titles)-1] != "Edit description?" {
		t.Fatalf("popups %v", h.ui.titles)
	}
	h.do((*app.App).EditDescription, true, "Edited") // yes, then a change: saved without asking again
	if got := srv.Task("t1").MarkdownDescription; got != "Edited" {
		t.Fatalf("description = %q", got)
	}
}

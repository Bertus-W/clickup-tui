package render

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

func task(id string, statusType string, order float64, parent string) *clickup.Task {
	return &clickup.Task{ID: id, Name: strings.ToUpper(id), Status: clickup.Status{Type: statusType}, OrderIndex: clickup.FlexFloat(order), Parent: clickup.FlexString(parent)}
}

func TestOrderedNestsSubtasks(t *testing.T) {
	rows := Ordered([]*clickup.Task{task("a", "open", 1, ""), task("b", "open", 2, "a"), task("c", "done", 0, ""), task("d", "open", 0, "")})
	var got []string
	for _, r := range rows {
		got = append(got, strings.Repeat(">", r.Depth)+r.Task.ID)
	}
	if strings.Join(got, " ") != "d a >b c" {
		t.Fatalf("order = %v", got)
	}
}

func TestHotkeys(t *testing.T) {
	if got := string(Hotkeys([]string{"Scope", "Severity", "Score"}, "jkq")); got != "sec" {
		t.Fatalf("hotkeys = %q", got)
	}
}

func severity(value string) *clickup.Task {
	return &clickup.Task{ID: "t", Name: "Fix it", Status: clickup.Status{Status: "to do"}, CustomFields: []clickup.CustomField{{
		ID: "sev", Name: "Severity", Type: "drop_down", Value: json.RawMessage(value),
		TypeConfig: clickup.TypeConfig{Options: []clickup.Option{
			{ID: "o1", Name: "Critical", Color: "#e50000", OrderIndex: 0},
			{ID: "o2", Name: "Minor", Color: "#f9d900", OrderIndex: 1},
		}},
	}}}
}

// The API reports dropdowns by orderindex; optimistic edits store the option id. Both render.
func TestDropdownValueByOrderIndexOrID(t *testing.T) {
	for _, value := range []string{"1", `"o2"`} {
		f := &severity(value).CustomFields[0]
		if got := style.Strip(FieldText(f)); got != " Minor " {
			t.Errorf("value %s renders %q", value, got)
		}
	}
}

func TestTableShowsDropdownColumnsWhenThereIsRoom(t *testing.T) {
	tasks := []*clickup.Task{severity("0")}
	wide := NewTable(tasks, 90, false)
	if len(wide.Dropdowns) != 1 {
		t.Fatalf("no dropdown column at width 90")
	}
	line := wide.Line(Row{Task: tasks[0]}, time.Now())
	if style.Width(line) != 90 || !strings.Contains(line, "48;2;229;0;0m Critical ") {
		t.Fatalf("line (%d wide) = %q", style.Width(line), line)
	}
	if narrow := NewTable(tasks, 50, false); len(narrow.Dropdowns) != 0 {
		t.Fatalf("dropdown column squeezes the name at width 50")
	}
}

func TestMarkdownSubset(t *testing.T) {
	out := style.Strip(Markdown("# Title\n- item with `code`\n```\nraw\n```\n**bold**", 80))
	want := "Title\n• item with  code \n" + " raw" + strings.Repeat(" ", 76) + "\nbold\n"
	if out != want {
		t.Fatalf("markdown = %q, want %q", out, want)
	}
}

func TestMarkdownBlocksAndSpans(t *testing.T) {
	md := "- [ ] open item\n- [x] done item\n1. first\n> quoted\n---\n" +
		"see [the docs](https://example.com/docs) or https://x.test/a, *soft* and _also_, ~~gone~~, ask @alice\n" +
		"snake_case_name and a*b*c stay as written"
	out := Markdown(md, 80)
	plain := style.Strip(out)
	for _, want := range []string{"[ ] open item", "[✓] done item", "1. first", "│ quoted", "────",
		"see the docs or https://x.test/a,", "snake_case_name and a*b*c stay as written"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	for _, want := range []string{style.Italic("soft"), style.Italic("also"), style.Strike("gone"), style.BoldCyan("@alice"),
		style.Link("https://example.com/docs", style.LinkText("the docs")),
		style.Link("https://x.test/a", style.LinkText("https://x.test/a"))} {
		if !strings.Contains(out, want) {
			t.Errorf("missing styled %q", style.Strip(want))
		}
	}
}

// Images (ClickUp writes pasted ones as ![](url)) and links become clickable labels, named after
// the file when they have no text.
func TestMarkdownImagesAndLinks(t *testing.T) {
	img := "https://t9012.p.clickup-attachments.com/t9012/f5f0/image%20one.png?view=open"
	for md, want := range map[string]string{
		"![](" + img + ")":                     style.Link(img, style.LinkText("[image: image one.png]")),
		"![Diagram](" + img + ")":              style.Link(img, style.LinkText("[image: Diagram]")),
		"[](https://example.com/)":             style.Link("https://example.com/", style.LinkText("example.com")),
		"[Spec](https://example.com/spec.pdf)": style.Link("https://example.com/spec.pdf", style.LinkText("Spec")),
	} {
		if got := strings.TrimSpace(Markdown(md, 80)); got != want {
			t.Errorf("Markdown(%q) = %q, want %q", md, got, want)
		}
	}
	// The hyperlink codes take no room: widths and plain text see only the label.
	label := Markdown("![]("+img+")", 80)
	if got := style.Strip(label); strings.TrimSpace(got) != "[image: image one.png]" {
		t.Errorf("plain text %q", got)
	}
	if w := style.Width(strings.TrimSpace(label)); w != len("[image: image one.png]") {
		t.Errorf("width %d", w)
	}
}

// Code blocks are grey boxes as wide as the panel, syntax highlighted like ClickUp, with long
// lines wrapped inside the box. Inline code gets a background too.
func TestCodeBlocks(t *testing.T) {
	out := Markdown("```go\nfmt.Println(\"a fairly long line of code\")\n\tx := 1\n```\nuse `go test`", 24)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	wantPlain := []string{
		" fmt.Println(\"a fairly  ",
		" long line of code\")    ",
		"     x := 1             ",
		"use  go test ",
	}
	if len(lines) != len(wantPlain) {
		t.Fatalf("lines = %q", lines)
	}
	for i, want := range wantPlain {
		if got := style.Strip(lines[i]); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
	// Go's syntax: the string, the := and the number each get their own colour, on the grey.
	colours := map[string]bool{}
	for _, m := range regexp.MustCompile(`\x1b\[(38;2;[0-9;]+);48;2;52;55;61m([^\x1b]+)`).FindAllStringSubmatch(lines[2], -1) {
		colours[m[1]] = true
	}
	if len(colours) < 3 {
		t.Errorf("x := 1 isn't highlighted: %q", lines[2])
	}
	if !strings.Contains(lines[3], style.Code(" go test ")) {
		t.Errorf("inline code: %q", lines[3])
	}
	// An unclosed block still ends up in a box.
	if got := style.Strip(Markdown("```\nstill code", 14)); got != " still code   \n" {
		t.Errorf("unclosed = %q", got)
	}
}

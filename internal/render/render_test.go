package render

import (
	"encoding/json"
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
	out := style.Strip(Markdown("# Title\n- item with `code`\n```\nraw\n```\n**bold**"))
	want := "Title\n• item with code\n  │ raw\nbold\n"
	if out != want {
		t.Fatalf("markdown = %q, want %q", out, want)
	}
}

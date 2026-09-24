package render

import (
	"encoding/json"
	"strings"
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// parts decodes a comment's pieces the way they arrive from ClickUp.
func parts(t *testing.T, raw string) []clickup.CommentPart {
	t.Helper()
	var p []clickup.CommentPart
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// What ClickUp stored for a formatted comment, verbatim.
const formatted = `[{"text":"plain "},{"attributes":{"color":"#e5484d"},"text":"red"},{"text":" "},
{"attributes":{"background":"#fff3a0"},"text":"highlighted"},{"text":" "},{"attributes":{"bold":true},"text":"bold"},
{"text":" "},{"attributes":{"code":true},"text":"code"},{"text":"\n"},{"text":"x := 1"},
{"attributes":{"code-block":{"code-block":"go"}},"text":"\n"}]`

func TestRichComment(t *testing.T) {
	out := Rich(parts(t, formatted), 20)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, want := range []string{
		style.Codes("38;2;229;72;77")("red"),
		style.Codes("48;2;255;243;160", "30")("highlighted"), // dark text on a light highlight
		style.Codes("1")("bold"),
		style.Code(" code "),
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first line lacks %q:\n%q", style.Strip(want), lines[0])
		}
	}
	if got := style.Strip(lines[0]); got != "plain red highlighted bold  code " {
		t.Errorf("plain first line = %q", got)
	}
	if got := style.Strip(lines[1]); got != " x := 1"+strings.Repeat(" ", 13) { // the code block, a box
		t.Errorf("code box = %q", got)
	}
}

func TestRichLines(t *testing.T) {
	raw := `[{"text":"Plan"},{"attributes":{"header":2},"text":"\n"},
{"text":"one"},{"attributes":{"list":"bullet"},"text":"\n"},
{"text":"first"},{"attributes":{"list":"ordered"},"text":"\n"},
{"text":"second"},{"attributes":{"list":"ordered"},"text":"\n"},
{"text":"done"},{"attributes":{"list":"checked"},"text":"\n"},
{"text":"todo"},{"attributes":{"list":"unchecked"},"text":"\n"},
{"text":"wise words"},{"attributes":{"blockquote":true},"text":"\n"},
{"text":"see "},{"attributes":{"link":"https://example.com"},"text":"the docs"},{"text":" "},
{"type":"tag","text":"@alice","user":{"id":7}},{"text":"\n"}]`
	got := style.Strip(Rich(parts(t, raw), 40))
	want := "Plan\n• one\n1. first\n2. second\n[✓] done\n[ ] todo\n│ wise words\nsee the docs @alice\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

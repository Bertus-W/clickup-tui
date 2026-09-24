package render

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/mattn/go-runewidth"

	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Code blocks look like ClickUp's: a grey box with syntax colours (One Dark, close to
// ClickUp's dark theme).
const codeBackground = "48;2;52;55;61"

var codeTheme = styles.Get("onedark")

// codeSpan is a piece of highlighted code and its SGR codes.
type codeSpan struct {
	text  string
	codes string
}

// codeBox draws a code block as a grey box as wide as width: syntax highlighted by its
// language (guessed when the block doesn't name one), long lines wrapped inside the box, one
// space of padding on each side.
func codeBox(lang string, lines []string, width int) string {
	width = max(width, 12)
	inner := width - 2
	var b strings.Builder
	for _, line := range highlight(lang, lines) {
		for {
			row, rest := cut(line, inner)
			b.WriteString(boxRow(row, inner))
			if len(rest) == 0 {
				break
			}
			line = rest
		}
	}
	return b.String()
}

// boxRow renders one row of the box: the spans on the grey, padded to inner columns.
func boxRow(spans []codeSpan, inner int) string {
	pad := style.Codes(codeBackground)
	var b strings.Builder
	b.WriteString(pad(" "))
	used := 0
	for _, s := range spans {
		b.WriteString(style.Codes(s.codes, codeBackground)(s.text))
		used += runewidth.StringWidth(s.text)
	}
	b.WriteString(pad(strings.Repeat(" ", max(inner-used, 0)+1)))
	return b.String() + "\n"
}

// cut splits a line of spans after width columns.
func cut(line []codeSpan, width int) (row, rest []codeSpan) {
	used := 0
	for i, s := range line {
		w := runewidth.StringWidth(s.text)
		if used+w <= width {
			row, used = append(row, s), used+w
			continue
		}
		head := runewidth.Truncate(s.text, width-used, "")
		if head != "" {
			row = append(row, codeSpan{head, s.codes})
		}
		rest = append([]codeSpan{{s.text[len(head):], s.codes}}, line[i+1:]...)
		if len(row) == 0 && head == "" { // a character wider than the box: take it anyway
			r := []rune(s.text)[0]
			row, rest[0].text = []codeSpan{{string(r), s.codes}}, string([]rune(s.text)[1:])
		}
		return row, rest
	}
	return row, nil
}

// highlight splits the code into lines of coloured spans.
func highlight(lang string, lines []string) [][]codeSpan {
	code := strings.ReplaceAll(strings.Join(lines, "\n"), "\t", "    ")
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	out := make([][]codeSpan, 1, len(lines))
	iter, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil { // show it uncoloured rather than not at all
		for i, l := range strings.Split(code, "\n") {
			if i > 0 {
				out = append(out, nil)
			}
			out[len(out)-1] = []codeSpan{{l, "38;2;220;220;220"}}
		}
		return out
	}
	for tok := iter(); tok != chroma.EOF; tok = iter() {
		codes := tokenCodes(codeTheme.Get(tok.Type))
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				out = append(out, nil)
			}
			if part != "" {
				out[len(out)-1] = append(out[len(out)-1], codeSpan{part, codes})
			}
		}
	}
	for len(out) > len(lines) && len(out[len(out)-1]) == 0 { // the lexer's closing newline
		out = out[:len(out)-1]
	}
	return out
}

func tokenCodes(e chroma.StyleEntry) string {
	codes := "38;2;220;220;220"
	if e.Colour.IsSet() {
		codes = fmt.Sprintf("38;2;%d;%d;%d", e.Colour.Red(), e.Colour.Green(), e.Colour.Blue())
	}
	if e.Bold == chroma.Yes {
		codes += ";1"
	}
	if e.Italic == chroma.Yes {
		codes += ";3"
	}
	return codes
}

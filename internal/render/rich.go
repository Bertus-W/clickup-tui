package render

import (
	"fmt"
	"strings"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// span is a run of text with its formatting; line is a line of spans plus the formatting of
// the line itself (Quill puts that on the "\n" that ends it: code-block, list, header, …).
type span struct {
	text  string
	attrs map[string]any
}

type richLine struct {
	spans []span
	attrs map[string]any
}

// Rich renders a formatted comment: text colours and highlights, bold, italic, underline,
// strikethrough, code, links and mentions, and lines as code blocks, lists, headers or quotes.
func Rich(parts []clickup.CommentPart, width int) string {
	var lines []richLine
	var cur []span
	for _, p := range parts {
		text, attrs := p.Text, p.Attributes
		if p.Type == "tag" {
			if text == "" && p.User != nil {
				text = "@" + p.User.Username
			}
			attrs = map[string]any{"mention": true}
		}
		for {
			i := strings.IndexByte(text, '\n')
			if i < 0 {
				if text != "" {
					cur = append(cur, span{text, attrs})
				}
				break
			}
			if i > 0 {
				cur = append(cur, span{text[:i], attrs})
			}
			lines = append(lines, richLine{cur, attrs})
			cur, text = nil, text[i+1:]
		}
	}
	if len(cur) > 0 {
		lines = append(lines, richLine{cur, nil})
	}

	var b strings.Builder
	number := 0 // of an ordered list
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if lang, ok := codeBlock(l.attrs); ok { // consecutive code lines make one box
			var code []string
			for ; i < len(lines); i++ {
				if _, ok := codeBlock(lines[i].attrs); !ok {
					break
				}
				code = append(code, plain(lines[i].spans))
			}
			i--
			b.WriteString(codeBox(lang, code, width))
			continue
		}
		text := ""
		for _, s := range l.spans {
			text += styled(s)
		}
		indent := strings.Repeat("  ", int(num(l.attrs["indent"])))
		list, _ := l.attrs["list"].(string)
		if list != "ordered" {
			number = 0
		}
		switch {
		case list == "bullet":
			text = indent + "• " + text
		case list == "ordered":
			number++
			text = indent + style.Dim(fmt.Sprintf("%d.", number)) + " " + text
		case list == "checked":
			text = indent + style.Green("[✓]") + " " + style.Dim(plain(l.spans))
		case list == "unchecked":
			text = indent + "[ ] " + text
		case l.attrs["header"] != nil:
			text = style.Bold(plain(l.spans))
		case l.attrs["blockquote"] != nil:
			text = style.Dim("│ ") + text
		}
		b.WriteString(text + "\n")
	}
	return b.String()
}

// codeBlock reports whether a line belongs to a code block, and its language if it names one
// ({"code-block": "go"}, {"code-block": {"code-block": "go"}} or {"code-block": true}).
func codeBlock(attrs map[string]any) (lang string, ok bool) {
	v, ok := attrs["code-block"]
	if !ok {
		return "", false
	}
	switch v := v.(type) {
	case string:
		lang = v
	case map[string]any:
		lang, _ = v["code-block"].(string)
	}
	if lang == "plain" || lang == "true" {
		lang = ""
	}
	return lang, true
}

func plain(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// styled renders one span with all its formatting in a single style (styles don't nest).
func styled(s span) string {
	a := s.attrs
	if url, ok := a["link"].(string); ok && url != "" {
		return style.Link(url, style.LinkText(s.text))
	}
	if a["code"] == true {
		return style.Code(" " + s.text + " ")
	}
	if a["mention"] == true {
		return style.BoldCyan(s.text)
	}
	var codes []string
	for attr, code := range map[string]string{"bold": "1", "italic": "3", "underline": "4", "strike": "9"} {
		if a[attr] == true {
			codes = append(codes, code)
		}
	}
	fg, hasFg := "", false
	if c, ok := a["color"].(string); ok {
		fg, hasFg = style.Color(c, false)
	}
	if c, ok := a["background"].(string); ok {
		if bg, ok := style.Color(c, true); ok {
			codes = append(codes, bg)
			if !hasFg { // readable text on the highlight, whatever the terminal's colours
				if hex, ok := style.Hex(c); ok && style.Contrast(hex) == "black" {
					fg, hasFg = "30", true
				} else {
					fg, hasFg = "97", true
				}
			}
		}
	}
	if hasFg {
		codes = append(codes, fg)
	}
	if len(codes) == 0 {
		return s.text
	}
	return style.Codes(codes...)(s.text)
}

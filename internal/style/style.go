// Package style builds ANSI-coloured strings for gocui views (which parse escape codes,
// including 24-bit colour), the same way lazygit renders its panels.
package style

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

const reset = "\x1b[0m"

// Style wraps text in escape codes. Styles don't nest: compose line fragments instead.
type Style func(string) string

func sgr(codes string) Style {
	return func(s string) string {
		if s == "" {
			return ""
		}
		return "\x1b[" + codes + "m" + s + reset
	}
}

var (
	Plain    Style = func(s string) string { return s }
	Bold           = sgr("1")
	Dim            = sgr("2")
	Red            = sgr("31")
	BoldRed        = sgr("1;31")
	Green          = sgr("32")
	Yellow         = sgr("33")
	Blue           = sgr("34")
	Magenta        = sgr("35")
	Cyan           = sgr("36")
	BoldCyan       = sgr("1;36")
	Reverse        = sgr("7")
	Italic         = sgr("3")
	LinkText       = sgr("4;36") // underlined cyan: something to click
	// Inline code: soft red on grey, like ClickUp. Code blocks are highlighted (render/code.go).
	Code   = sgr("38;2;235;120;120;48;2;52;55;61")
	Strike = sgr("2;9")
)

var hexPattern = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// Hex normalises a ClickUp colour (#abc or #aabbcc) to #aabbcc.
func Hex(value string) (string, bool) {
	m := hexPattern.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	h := m[1]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	return "#" + strings.ToLower(h), true
}

func rgb(hex string) (r, g, b int64) {
	r, _ = strconv.ParseInt(hex[1:3], 16, 0)
	g, _ = strconv.ParseInt(hex[3:5], 16, 0)
	b, _ = strconv.ParseInt(hex[5:7], 16, 0)
	return
}

// Fg colours text with a hex colour, falling back to fallback for invalid colours.
func Fg(color string, fallback Style) Style {
	hex, ok := Hex(color)
	if !ok {
		return fallback
	}
	r, g, b := rgb(hex)
	return sgr(fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
}

var rgbPattern = regexp.MustCompile(`^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)`)

// Color is the SGR code for a colour ClickUp's editor writes: #rgb, #rrggbb or rgb(r, g, b),
// as a foreground or (bg) background colour.
func Color(value string, bg bool) (code string, ok bool) {
	var r, g, b int64
	if hex, ok := Hex(value); ok {
		r, g, b = rgb(hex)
	} else if m := rgbPattern.FindStringSubmatch(value); m != nil {
		r, _ = strconv.ParseInt(m[1], 10, 0)
		g, _ = strconv.ParseInt(m[2], 10, 0)
		b, _ = strconv.ParseInt(m[3], 10, 0)
	} else {
		return "", false
	}
	layer := 38
	if bg {
		layer = 48
	}
	return fmt.Sprintf("%d;2;%d;%d;%d", layer, r, g, b), true
}

// Codes combines SGR codes (e.g. "1" bold and a Color) into one style.
func Codes(codes ...string) Style { return sgr(strings.Join(codes, ";")) }

// Contrast picks black or white text for a background colour.
func Contrast(hex string) string {
	r, g, b := rgb(hex)
	if 299*r+587*g+114*b > 150_000 {
		return "black"
	}
	return "white"
}

// Chip renders a filled, colour-coded label like ClickUp's dropdown chips.
func Chip(label, color string) string {
	hex, ok := Hex(color)
	if !ok {
		return Reverse(" " + label + " ")
	}
	r, g, b := rgb(hex)
	fg := "30" // black
	if Contrast(hex) == "white" {
		fg = "97"
	}
	return sgr(fmt.Sprintf("%s;48;2;%d;%d;%d", fg, r, g, b))(" " + label + " ")
}

// ansi matches colour codes and OSC 8 hyperlinks (see Link).
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;[^\x1b\x07]*(?:\x1b\\|\x07)`)

// Link makes text a hyperlink to url (OSC 8): gocui reports clicks on it, and terminals that
// support it open it on cmd/ctrl-click.
func Link(url, text string) string {
	url = strings.Map(func(r rune) rune { // no control characters inside the escape code
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, url)
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// Strip removes escape codes.
func Strip(s string) string { return ansi.ReplaceAllString(s, "") }

// Width is the display width of s, ignoring escape codes.
func Width(s string) int { return runewidth.StringWidth(Strip(s)) }

// Cell is plain text plus a style, so it can be truncated before colouring.
type Cell struct {
	Text  string
	Style Style
}

// Fit truncates and pads the cell to exactly width display columns.
func (c Cell) Fit(width int) string {
	text := runewidth.Truncate(c.Text, width, "…")
	pad := strings.Repeat(" ", max(0, width-runewidth.StringWidth(text)))
	if c.Style == nil {
		return text + pad
	}
	return c.Style(text) + pad
}

// Raw is a pre-styled cell (e.g. a chip) padded to width; it must already fit.
func Raw(styled string, width int) string {
	return styled + strings.Repeat(" ", max(0, width-Width(styled)))
}

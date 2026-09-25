package render

import (
	"cmp"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// Detail renders the task panel: title, meta, custom fields, description, subtasks and comments.
// width is the panel's inner width: code blocks are boxes that span it.
func Detail(t *clickup.Task, comments []clickup.Comment, now time.Time, width int) string {
	// Every property is Copyable: clicking it copies it (the underline shows on hover).
	copyable := style.Copyable
	var b strings.Builder
	b.WriteString("\n" + copyable(t.Name, style.Bold(t.Name)) + "\n\n")

	status := StatusCell(t)
	b.WriteString(copyable(t.Status.Status, status.Fit(style.Width(status.Text))))
	if t.Priority != nil {
		b.WriteString("   " + copyable(t.Priority.Priority, PriorityFlag(t, true)))
	}
	if due := DueCell(t, now); due.Text != "" {
		day, _ := Millis(t.DueDate)
		b.WriteString("   due " + copyable(day.Format(time.DateOnly), due.Fit(len(due.Text))))
	}
	b.WriteString("\n")
	people := make([]string, len(t.Assignees))
	for i, a := range t.Assignees {
		people[i] = copyable(a.Username, a.Username)
	}
	// No emoji here: terminals disagree on their width, which breaks panel borders.
	b.WriteString(style.Dim("assigned ") + cmp.Or(strings.Join(people, ", "), style.Dim("nobody")))
	for _, tag := range t.Tags {
		b.WriteString(" " + copyable(tag.Name, style.Chip("#"+tag.Name, tag.TagBg)))
	}
	b.WriteString("\n")
	meta := []string{copyable(t.Label(), style.Dim(t.Label()))}
	if t.List.Name != "" {
		meta = append(meta, copyable(t.List.Name, style.Dim(t.List.Name)))
	}
	if updated, ok := Millis(t.DateUpdated); ok {
		when := updated.Format("2006-01-02 15:04")
		meta = append(meta, style.Dim("updated ")+copyable(when, style.Dim(when)))
	}
	if spent := t.TimeSpent.Int(); spent > 0 {
		hours := parse.Hours(time.Duration(spent) * time.Millisecond)
		meta = append(meta, style.Dim("tracked ")+copyable(hours, style.Dim(hours)))
	}
	b.WriteString(strings.Join(meta, style.Dim("  ·  ")) + "\n\n")

	if len(t.CustomFields) > 0 {
		b.WriteString(FieldsBlock(t) + "\n")
	}
	b.WriteString(Markdown(cmp.Or(strings.TrimSpace(t.Body()), "*No description*"), width))

	if len(t.Subtasks) > 0 {
		b.WriteString("\n" + style.Bold("Subtasks") + "\n")
		for _, s := range t.Subtasks {
			b.WriteString("  " + style.Fg(s.Status.Color, style.Dim)("●") + " " + s.Name + "\n")
		}
	}

	b.WriteString("\n")
	if comments == nil {
		b.WriteString(style.Bold("Comments") + "\n" + style.Dim("loading…") + "\n")
		return b.String()
	}
	count := len(comments)
	for _, c := range comments {
		count += len(c.Replies)
	}
	b.WriteString(style.Bold("Comments ("+strconv.Itoa(count)+")") + "\n")
	for _, c := range byDate(comments) {
		b.WriteString("\n" + commentBlock(c, "", width))
		for _, r := range byDate(c.Replies) { // a thread's replies, indented under it
			b.WriteString(commentBlock(r, "  ↳ ", width))
		}
	}
	return b.String()
}

func byDate(comments []clickup.Comment) []clickup.Comment {
	return slices.SortedStableFunc(slices.Values(comments), func(a, b clickup.Comment) int {
		return cmp.Compare(a.Date.Int(), b.Date.Int())
	})
}

// commentBlock is a comment's header and text; replies get a marker and their text indented.
func commentBlock(c clickup.Comment, marker string, width int) string {
	when := ""
	if t, ok := Millis(c.Date); ok {
		when = t.Format("2006-01-02 15:04")
	}
	header := style.Dim(marker) + style.BoldCyan(cmp.Or(c.User.Username, "?")) + " " + style.Dim(when)
	if c.Pending {
		header += " " + style.Yellow("sending…")
	}
	indent := strings.Repeat(" ", style.Width(marker))
	text := Markdown(strings.TrimSpace(c.Text()), width-len(indent))
	if c.Formatted() { // written in ClickUp's editor: its formatting says more than markdown
		text = Rich(c.Parts, width-len(indent))
	}
	if marker != "" {
		var lines []string
		for line := range strings.Lines(text) {
			lines = append(lines, indent+line)
		}
		text = strings.Join(lines, "")
	}
	return header + "\n" + text
}

var (
	heading   = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	checkbox  = regexp.MustCompile(`^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$`)
	bullet    = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	numbered  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	quote     = regexp.MustCompile(`^\s*>\s?(.*)$`)
	rule      = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	inlineTok = regexp.MustCompile("`(?P<code>[^`]+)`" +
		`|!\[(?P<alt>[^\]]*)\]\((?P<img>[^)\s]+)\)` + // an image, often without a name
		`|\[(?P<text>[^\]]*)\]\((?P<href>[^)\s]+)\)` +
		`|(?P<url>https?://[^\s)>\]]+)` +
		`|\*\*(?P<bold>[^*]+)\*\*` +
		`|~~(?P<strike>[^~]+)~~` +
		`|(?P<pre>^|[\s(])[*_](?P<em>[^*_\s][^*_]*?)[*_](?P<post>$|[\s).,!?:;])` + // italic, and what surrounds it
		`|(?P<space>^|\s)(?P<mention>@\w[\w.-]*)`)
)

// Markdown renders the markdown people write in ClickUp for the terminal: headings, bold,
// italic, strikethrough, code, links, bullets, numbered lists, checklists, quotes, rules,
// fenced code blocks and @mentions. Anything else is shown as written. Code blocks are grey
// boxes width columns wide.
func Markdown(md string, width int) string {
	var b strings.Builder
	var code []string // the lines of the code block being read
	lang, inFence := "", false
	for line := range strings.Lines(md) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			if inFence {
				b.WriteString(codeBox(lang, code, width))
				code = nil
			}
			inFence = !inFence
			lang = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "```"))
		case inFence:
			code = append(code, line)
		default:
			b.WriteString(block(line) + "\n")
		}
	}
	if inFence { // an unclosed block runs to the end
		b.WriteString(codeBox(lang, code, width))
	}
	return b.String()
}

// block renders one line outside code fences.
func block(line string) string {
	if rule.MatchString(line) {
		return style.Dim(strings.Repeat("─", 24))
	}
	if m := heading.FindStringSubmatch(line); m != nil {
		return style.Bold(m[2])
	}
	if m := checkbox.FindStringSubmatch(line); m != nil {
		if m[2] == " " {
			return m[1] + "[ ] " + inline(m[3])
		}
		return m[1] + style.Green("[✓]") + " " + style.Dim(m[3])
	}
	if m := bullet.FindStringSubmatch(line); m != nil {
		return m[1] + "• " + inline(m[2])
	}
	if m := numbered.FindStringSubmatch(line); m != nil {
		return m[1] + style.Dim(m[2]+".") + " " + inline(m[3])
	}
	if m := quote.FindStringSubmatch(line); m != nil {
		return style.Dim("│ ") + inline(m[1])
	}
	return inline(line)
}

// inline styles the spans of one line. Styles don't nest, so each span gets one style. Links
// and images become clickable labels (style.Link) instead of showing their URL.
func inline(line string) string {
	return inlineTok.ReplaceAllStringFunc(line, func(tok string) string {
		m := inlineTok.FindStringSubmatch(tok)
		g := func(name string) string { return m[inlineTok.SubexpIndex(name)] }
		switch {
		case g("code") != "":
			return style.Code(" " + g("code") + " ")
		case g("img") != "":
			return style.Link(g("img"), style.LinkText("[image: "+cmp.Or(g("alt"), linkName(g("img")))+"]"))
		case g("href") != "":
			return style.Link(g("href"), style.LinkText(cmp.Or(g("text"), linkName(g("href")))))
		case g("url") != "":
			u := strings.TrimRight(g("url"), ".,;:!?") // "see https://x.test/a, then …"
			return style.Link(u, style.LinkText(u)) + g("url")[len(u):]
		case g("bold") != "":
			return style.Bold(g("bold"))
		case g("strike") != "":
			return style.Strike(g("strike"))
		case g("em") != "":
			return g("pre") + style.Italic(g("em")) + g("post")
		case g("mention") != "":
			return g("space") + style.BoldCyan(g("mention"))
		}
		return tok
	})
}

// linkName names a link that has no text: the file it points at (ClickUp's attachment URLs
// end in the file name, e.g. image.png), else its host.
func linkName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if name, err := url.PathUnescape(path.Base(u.Path)); err == nil && name != "." && name != "/" {
		return name
	}
	return cmp.Or(u.Host, raw)
}

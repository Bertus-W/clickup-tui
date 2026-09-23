package render

import (
	"cmp"
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
func Detail(t *clickup.Task, comments []clickup.Comment, now time.Time) string {
	var b strings.Builder
	b.WriteString("\n" + style.Bold(t.Name) + "\n\n")

	b.WriteString(StatusCell(t).Fit(style.Width(StatusCell(t).Text)))
	if t.Priority != nil {
		b.WriteString("   " + PriorityFlag(t, true))
	}
	if due := DueCell(t, now); due.Text != "" {
		b.WriteString("   due " + due.Fit(len(due.Text)))
	}
	b.WriteString("\n")
	people := make([]string, len(t.Assignees))
	for i, a := range t.Assignees {
		people[i] = a.Username
	}
	// No emoji here: terminals disagree on their width, which breaks panel borders.
	b.WriteString(style.Dim("assigned ") + cmp.Or(strings.Join(people, ", "), style.Dim("nobody")))
	for _, tag := range t.Tags {
		b.WriteString(" " + style.Chip("#"+tag.Name, tag.TagBg))
	}
	b.WriteString("\n")
	meta := []string{t.Label()}
	if t.List.Name != "" {
		meta = append(meta, t.List.Name)
	}
	if updated, ok := Millis(t.DateUpdated); ok {
		meta = append(meta, "updated "+updated.Format("2006-01-02 15:04"))
	}
	if spent := t.TimeSpent.Int(); spent > 0 {
		meta = append(meta, "tracked "+parse.Hours(time.Duration(spent)*time.Millisecond))
	}
	b.WriteString(style.Dim(strings.Join(meta, "  ·  ")) + "\n\n")

	if len(t.CustomFields) > 0 {
		b.WriteString(FieldsBlock(t) + "\n")
	}
	b.WriteString(Markdown(cmp.Or(strings.TrimSpace(t.Body()), "*No description*")))

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
	b.WriteString(style.Bold("Comments ("+strconv.Itoa(len(comments))+")") + "\n")
	sorted := slices.SortedStableFunc(slices.Values(comments), func(a, b clickup.Comment) int {
		return cmp.Compare(a.Date.Int(), b.Date.Int())
	})
	for _, c := range sorted {
		when := ""
		if t, ok := Millis(c.Date); ok {
			when = t.Format("2006-01-02 15:04")
		}
		header := style.BoldCyan(cmp.Or(c.User.Username, "?")) + " " + style.Dim(when)
		if c.Pending {
			header += " " + style.Yellow("sending…")
		}
		b.WriteString("\n" + header + "\n" + Markdown(strings.TrimSpace(c.Text())))
	}
	return b.String()
}

var (
	heading   = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	checkbox  = regexp.MustCompile(`^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$`)
	bullet    = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	numbered  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	quote     = regexp.MustCompile(`^\s*>\s?(.*)$`)
	rule      = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	inlineTok = regexp.MustCompile("`([^`]+)`" + // 1: code
		`|\[([^\]]+)\]\(([^)\s]+)\)` + // 2, 3: [text](url)
		`|(https?://[^\s)>\]]+)` + // 4: a bare URL
		`|\*\*([^*]+)\*\*` + // 5: bold
		`|~~([^~]+)~~` + // 6: strikethrough
		`|(^|[\s(])[*_]([^*_\s][^*_]*?)[*_]($|[\s).,!?:;])` + // 7, 8, 9: italic with what surrounds it
		`|(^|\s)(@\w[\w.-]*)`) // 10, 11: a mention
)

// Markdown renders the markdown people write in ClickUp for the terminal: headings, bold,
// italic, strikethrough, code, links, bullets, numbered lists, checklists, quotes, rules,
// fenced code blocks and @mentions. Anything else is shown as written.
func Markdown(md string) string {
	var b strings.Builder
	inFence := false
	for line := range strings.Lines(md) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			inFence = !inFence
			continue
		case inFence:
			b.WriteString(style.Dim("  │ ") + style.Yellow(line) + "\n")
			continue
		}
		b.WriteString(block(line) + "\n")
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

// inline styles the spans of one line. Styles don't nest, so each span gets one style.
func inline(line string) string {
	return inlineTok.ReplaceAllStringFunc(line, func(tok string) string {
		m := inlineTok.FindStringSubmatch(tok)
		switch {
		case m[1] != "":
			return style.Yellow(m[1])
		case m[2] != "":
			return style.Cyan(m[2]) + style.Dim(" ("+m[3]+")")
		case m[4] != "":
			url := strings.TrimRight(m[4], ".,;:!?") // "see https://x.test/a, then …"
			return style.Cyan(url) + m[4][len(url):]
		case m[5] != "":
			return style.Bold(m[5])
		case m[6] != "":
			return style.Strike(m[6])
		case m[8] != "":
			return m[7] + style.Italic(m[8]) + m[9]
		case m[11] != "":
			return m[10] + style.BoldCyan(m[11])
		}
		return tok
	})
}

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
		b.WriteString("\n" + header + "\n" + Markdown(strings.TrimSpace(c.CommentText)))
	}
	return b.String()
}

var (
	inlineCode = regexp.MustCompile("`([^`]+)`")
	boldText   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	heading    = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bullet     = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
)

// Markdown renders a simplified subset for the terminal: headings, bold, inline code,
// bullets and fenced code blocks. Everything else is shown as written.
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
		if m := heading.FindStringSubmatch(line); m != nil {
			b.WriteString(style.Bold(m[2]) + "\n")
			continue
		}
		if m := bullet.FindStringSubmatch(line); m != nil {
			line = m[1] + "• " + m[2]
		}
		b.WriteString(inline(line) + "\n")
	}
	return b.String()
}

func inline(line string) string {
	line = inlineCode.ReplaceAllStringFunc(line, func(s string) string {
		return style.Yellow(inlineCode.FindStringSubmatch(s)[1])
	})
	return boldText.ReplaceAllStringFunc(line, func(s string) string {
		return style.Bold(boldText.FindStringSubmatch(s)[1])
	})
}

package app

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

// A mention is "@" plus a member's username (any case), its first word when no other member
// shares it, or the start of a word of its name (3+ letters) when that fits one member only:
// "@alice", "@Bertus Wisman", "@bertus", "@wism". Mentions in code (`x`, ``` blocks) don't
// count, nor an "@" inside a word (a@b.c).

// mention is one found in a text: text[start:end] names user.
type mention struct {
	start, end int
	user       clickup.User
}

// findMentions lists the mentions in text, and the @words that look like one but name nobody
// (or more than one member), so they can be pointed out.
func findMentions(text string, members []clickup.User) (found []mention, unknown []string) {
	fence, code := false, false
	for i := 0; i < len(text); i++ {
		if i == 0 || text[i-1] == '\n' {
			code = false
			line, _, _ := strings.Cut(text[i:], "\n")
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fence = !fence
				i += len(line) - 1
				continue
			}
		}
		switch {
		case fence:
			continue
		case text[i] == '`':
			code = !code
		}
		if code || text[i] != '@' || (i > 0 && !isSpace(text[i-1])) {
			continue
		}
		u, n, ok := mentionAt(text[i+1:], members)
		if !ok {
			if w := leadingWord(text[i+1:]); w != "" {
				unknown = append(unknown, "@"+w)
			}
			continue
		}
		found = append(found, mention{i, i + 1 + n, u})
		i += n
	}
	return found, unknown
}

// mentionParts splits a comment into text and the tagged @mentions of workspace members.
func mentionParts(text string, members []clickup.User) (parts []clickup.CommentPart, mentioned []clickup.User, unknown []string) {
	found, unknown := findMentions(text, members)
	start := 0 // of the text not yet emitted
	for _, m := range found {
		if start < m.start {
			parts = append(parts, clickup.CommentPart{Text: text[start:m.start]})
		}
		// The tag carries its text too: without it ClickUp stores the mention unresolved.
		parts = append(parts, clickup.CommentPart{Type: "tag", Text: "@" + m.user.Username, User: &clickup.CommentUser{ID: m.user.ID}})
		mentioned = append(mentioned, m.user)
		start = m.end
	}
	if start < len(text) {
		parts = append(parts, clickup.CommentPart{Text: text[start:]})
	}
	return parts, mentioned, unknown
}

// mentionMarkdown turns the @mentions in a description into ClickUp's markdown for them,
// [@Name](#user_mention#id), which ClickUp stores as a real mention.
func mentionMarkdown(text string, members []clickup.User) (md string, mentioned []clickup.User, unknown []string) {
	found, unknown := findMentions(text, members)
	var b strings.Builder
	start := 0
	for _, m := range found {
		fmt.Fprintf(&b, "%s[@%s](#user_mention#%d)", text[start:m.start], m.user.Username, m.user.ID)
		mentioned = append(mentioned, m.user)
		start = m.end
	}
	b.WriteString(text[start:])
	return b.String(), mentioned, unknown
}

var mentionLink = regexp.MustCompile(`\[(@[^\]\n]*)\]\(#user_mention#\d+\)`)

// plainMentions is the reverse of mentionMarkdown, for editing: "[@Name](#user_mention#id)"
// reads "@Name", which saving turns back into the mention.
func plainMentions(md string) string { return mentionLink.ReplaceAllString(md, "$1") }

func isSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '(' }

// leadingWord is the letters and digits rest starts with.
func leadingWord(rest string) string {
	end := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// mentionAt finds the member named at the start of rest: the longest full username, else a
// unique first name, else a unique start of a name's word. n is the length of the name in rest.
func mentionAt(rest string, members []clickup.User) (u clickup.User, n int, ok bool) {
	ends := func(name string) bool {
		if len(rest) < len(name) || !strings.EqualFold(rest[:len(name)], name) {
			return false
		}
		r, _ := utf8.DecodeRuneInString(rest[len(name):])
		return len(rest) == len(name) || !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}
	for _, m := range members {
		if m.Username != "" && len(m.Username) > n && ends(m.Username) {
			u, n, ok = m, len(m.Username), true
		}
	}
	if ok {
		return u, n, true
	}
	var found []clickup.User
	for _, m := range members {
		if first, _, _ := strings.Cut(m.Username, " "); first != "" && ends(first) {
			found = append(found, m)
		}
	}
	if len(found) == 1 {
		first, _, _ := strings.Cut(found[0].Username, " ")
		return found[0], len(first), true
	}
	if len(found) > 1 {
		return clickup.User{}, 0, false
	}
	word := leadingWord(rest)
	if utf8.RuneCountInString(word) < 3 {
		return clickup.User{}, 0, false
	}
	for _, m := range members {
		if nameWordStarts(m.Username, word) {
			found = append(found, m)
		}
	}
	if len(found) == 1 {
		return found[0], len(word), true
	}
	return clickup.User{}, 0, false
}

// nameWordStarts reports whether a word of name starts with prefix, ignoring case.
func nameWordStarts(name, prefix string) bool {
	return slices.ContainsFunc(strings.Fields(name), func(w string) bool {
		return strings.HasPrefix(strings.ToLower(w), strings.ToLower(prefix))
	})
}

// maxSuggestions caps the members suggested for an @mention.
const maxSuggestions = 8

// mentionSuggestions suggests members for the "@partial" at the end of text: those whose name
// starts with it, then those with a later word that does. at is the index of the "@".
func mentionSuggestions(text string, members []clickup.User) (at int, names []string) {
	at = strings.LastIndexByte(text, '@')
	if at < 0 || (at > 0 && !isSpace(text[at-1])) {
		return 0, nil
	}
	partial := strings.ToLower(text[at+1:])
	if strings.ContainsAny(partial, "\n\t") {
		return 0, nil
	}
	var first, later []string
	for _, m := range members {
		name := strings.ToLower(m.Username)
		switch {
		case m.Username == "":
		case strings.HasPrefix(name, partial):
			first = append(first, m.Username)
		case !strings.Contains(partial, " ") && partial != "" && nameWordStarts(name, partial):
			later = append(later, m.Username)
		}
	}
	byName := func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) }
	slices.SortFunc(first, byName)
	slices.SortFunc(later, byName)
	names = slices.Concat(first, later)
	if len(names) == 1 && strings.EqualFold(names[0], partial) {
		return 0, nil // already written in full
	}
	return at, names[:min(len(names), maxSuggestions)]
}

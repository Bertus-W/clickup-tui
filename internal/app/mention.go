package app

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

// mentionParts splits a comment into text and @mentions of workspace members. A mention is
// "@" plus a member's username (any case), or just its first word when no other member
// shares it: "@alice", "@Bertus Wisman", "@bertus".
func mentionParts(text string, members []clickup.User) (parts []clickup.CommentPart, mentioned []clickup.User) {
	start := 0 // of the text not yet emitted
	for i := 0; i < len(text); i++ {
		if text[i] != '@' || (i > 0 && !isSpace(text[i-1])) {
			continue
		}
		u, n, ok := mentionAt(text[i+1:], members)
		if !ok {
			continue
		}
		if start < i {
			parts = append(parts, clickup.CommentPart{Text: text[start:i]})
		}
		// The tag carries its text too: without it ClickUp stores the mention unresolved.
		parts = append(parts, clickup.CommentPart{Type: "tag", Text: "@" + u.Username, User: &clickup.CommentUser{ID: u.ID}})
		mentioned = append(mentioned, u)
		i += n
		start = i + 1
	}
	if start < len(text) {
		parts = append(parts, clickup.CommentPart{Text: text[start:]})
	}
	return parts, mentioned
}

func isSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '(' }

// mentionAt finds the member named at the start of rest: the longest full username, else a
// unique first name. n is the length of the name in rest.
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
	return clickup.User{}, 0, false
}

// completeMention completes the "@partial" at the end of text to a member's name: in full
// when one member matches, else as far as all matches agree.
func completeMention(text string, members []clickup.User) string {
	at := strings.LastIndexByte(text, '@')
	if at < 0 || (at > 0 && !isSpace(text[at-1])) {
		return text
	}
	partial := strings.ToLower(text[at+1:])
	if strings.ContainsAny(partial, " \n\t") {
		return text
	}
	var names []string
	for _, m := range members {
		if strings.HasPrefix(strings.ToLower(m.Username), partial) {
			names = append(names, m.Username)
		}
	}
	switch len(names) {
	case 0:
		return text
	case 1:
		return text[:at+1] + names[0] + " "
	}
	common := names[0]
	for _, name := range names[1:] {
		for !strings.HasPrefix(strings.ToLower(name), strings.ToLower(common)) {
			common = common[:len(common)-1]
		}
	}
	if len(common) <= len(partial) {
		return text
	}
	return text[:at+1] + common
}

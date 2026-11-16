package app

import (
	"slices"
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

var people = []clickup.User{{ID: 1, Username: "Bertus Wisman"}, {ID: 2, Username: "alice"}, {ID: 3, Username: "Alicia Keys"}}

func TestMentionParts(t *testing.T) {
	parts, who, unknown := mentionParts("hi @alice and @bertus wisman, see @Bertus. mail a@b.c @nobody `@alice` @wism", people)
	if len(who) != 4 || who[0].ID != 2 || who[1].ID != 1 || who[2].ID != 1 || who[3].ID != 1 {
		t.Fatalf("mentioned %+v", who)
	}
	var text string
	for _, p := range parts {
		if p.Type == "tag" {
			text += "<" + string(rune('0'+p.User.ID)) + ">"
		} else {
			text += p.Text
		}
	}
	if want := "hi <2> and <1>, see <1>. mail a@b.c @nobody `@alice` <1>"; text != want {
		t.Fatalf("parts read %q, want %q", text, want)
	}
	if !slices.Equal(unknown, []string{"@nobody"}) {
		t.Fatalf("unknown %q", unknown)
	}
	// "@ali" starts two names; "@be" is too short to guess from.
	if _, who, unknown := mentionParts("@ali @be", people); len(who) != 0 || len(unknown) != 2 {
		t.Fatalf("mentioned %+v, unknown %q", who, unknown)
	}
}

func TestMentionMarkdown(t *testing.T) {
	text := "Ask @alice or @keys.\n\n```\n@alice in code\n```\n"
	md, who, _ := mentionMarkdown(text, people)
	want := "Ask [@alice](#user_mention#2) or [@Alicia Keys](#user_mention#3).\n\n```\n@alice in code\n```\n"
	if md != want || len(who) != 2 {
		t.Fatalf("markdown %q (%d mentioned), want %q", md, len(who), want)
	}
	// In the editor the mentions read as written, and saving that again gives the same markdown.
	plain := plainMentions(md)
	if plain != "Ask @alice or @Alicia Keys.\n\n```\n@alice in code\n```\n" {
		t.Fatalf("plain %q", plain)
	}
	if again, _, _ := mentionMarkdown(plain, people); again != md {
		t.Fatalf("round trip %q", again)
	}
}

func TestMentionSuggestions(t *testing.T) {
	for text, want := range map[string][]string{
		"ping @":         {"alice", "Alicia Keys", "Bertus Wisman"},
		"ping @ali":      {"alice", "Alicia Keys"},
		"ping @wis":      {"Bertus Wisman"}, // a later word of the name
		"ping @Bertus W": {"Bertus Wisman"},
		"ping @alice":    nil, // written in full
		"ping @zed":      nil,
		"mail a@b":       nil,
		"@alice hi":      nil,
	} {
		at, got := mentionSuggestions(text, people)
		if !slices.Equal(got, want) || (got != nil && text[at] != '@') {
			t.Errorf("suggest(%q) = %d %q, want %q", text, at, got, want)
		}
	}
}

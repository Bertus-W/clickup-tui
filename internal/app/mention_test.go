package app

import (
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
)

var people = []clickup.User{{ID: 1, Username: "Bertus Wisman"}, {ID: 2, Username: "alice"}, {ID: 3, Username: "Alicia Keys"}}

func TestMentionParts(t *testing.T) {
	parts, who := mentionParts("hi @alice and @bertus wisman, see @Bertus. mail a@b.c @nobody", people)
	if len(who) != 3 || who[0].ID != 2 || who[1].ID != 1 || who[2].ID != 1 {
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
	if want := "hi <2> and <1>, see <1>. mail a@b.c @nobody"; text != want {
		t.Fatalf("parts read %q, want %q", text, want)
	}
	// "@ali" is neither a name nor a unique first name.
	if _, who := mentionParts("@ali", people); len(who) != 0 {
		t.Fatalf("@ali mentioned %+v", who)
	}
}

func TestCompleteMention(t *testing.T) {
	for text, want := range map[string]string{
		"ping @be":    "ping @Bertus Wisman ",
		"ping @ali":   "ping @alic", // alice and Alicia agree this far
		"ping @alice": "ping @alice ",
		"ping @zed":   "ping @zed",
		"mail a@b":    "mail a@b",
	} {
		if got := completeMention(text, people); got != want {
			t.Errorf("complete(%q) = %q, want %q", text, got, want)
		}
	}
}

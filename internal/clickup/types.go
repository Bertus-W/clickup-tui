// Package clickup is a small client for the ClickUp REST API v2.
package clickup

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// FlexString decodes JSON strings, numbers and null alike; ClickUp is inconsistent about ids and timestamps.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	switch {
	case bytes.Equal(b, []byte("null")):
		*f = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
	default:
		*f = FlexString(b)
	}
	return nil
}

func (f FlexString) String() string { return string(f) }

// Int parses the value as an integer (0 when empty or invalid).
func (f FlexString) Int() int64 {
	n, _ := strconv.ParseInt(string(f), 10, 64)
	return n
}

// FlexFloat decodes numbers encoded as JSON numbers or strings ("1.00000").
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	var s FlexString
	if err := s.UnmarshalJSON(b); err != nil {
		return err
	}
	v, _ := strconv.ParseFloat(string(s), 64)
	*f = FlexFloat(v)
	return nil
}

type User struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Email          string `json:"email,omitzero"`
	Color          string `json:"color,omitzero"`
	Initials       string `json:"initials,omitzero"`
	ProfilePicture string `json:"profilePicture,omitzero"`
}

type Member struct {
	User User `json:"user"`
}

type Team struct {
	ID      FlexString `json:"id"`
	Name    string     `json:"name"`
	Members []Member   `json:"members,omitzero"`
}

type Status struct {
	ID         string    `json:"id,omitzero"`
	Status     string    `json:"status"`
	Type       string    `json:"type"`
	OrderIndex FlexFloat `json:"orderindex"`
	Color      string    `json:"color"`
}

// Closed reports whether the status counts as finished.
func (s Status) Closed() bool { return s.Type == "closed" || s.Type == "done" }

type Priority struct {
	ID       FlexString `json:"id"`
	Priority string     `json:"priority"`
	Color    string     `json:"color"`
}

type Tag struct {
	Name  string `json:"name"`
	TagBg string `json:"tag_bg,omitzero"`
	TagFg string `json:"tag_fg,omitzero"`
}

type Ref struct {
	ID   FlexString `json:"id"`
	Name string     `json:"name,omitzero"`
}

type Option struct {
	ID         string    `json:"id,omitzero"` // omitted when creating a field
	Name       string    `json:"name,omitzero"`
	Label      string    `json:"label,omitzero"` // labels fields use "label" instead of "name"
	Color      string    `json:"color,omitzero"`
	OrderIndex FlexFloat `json:"orderindex,omitzero"`
}

// Title is the option's display name, whichever key ClickUp used.
func (o Option) Title() string {
	if o.Name != "" {
		return o.Name
	}
	return o.Label
}

type TypeConfig struct {
	Options      []Option  `json:"options,omitzero"`
	Count        int       `json:"count,omitzero"`
	Start        FlexFloat `json:"start,omitzero"`
	End          FlexFloat `json:"end,omitzero"`
	CurrencyType string    `json:"currency_type,omitzero"`
}

type CustomField struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	TypeConfig TypeConfig      `json:"type_config"`
	Value      json.RawMessage `json:"value,omitzero"`
	Required   bool            `json:"required,omitzero"`
}

// IsSet reports whether the field holds a value.
func (f CustomField) IsSet() bool {
	v := bytes.TrimSpace(f.Value)
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`)) && !bytes.Equal(v, []byte("[]"))
}

type Task struct {
	ID                  string        `json:"id"`
	CustomID            string        `json:"custom_id,omitzero"`
	Name                string        `json:"name"`
	Status              Status        `json:"status"`
	Assignees           []User        `json:"assignees"`
	Tags                []Tag         `json:"tags,omitzero"`
	Priority            *Priority     `json:"priority"`
	DueDate             FlexString    `json:"due_date,omitzero"`
	OrderIndex          FlexFloat     `json:"orderindex"`
	Parent              FlexString    `json:"parent,omitzero"`
	DateUpdated         FlexString    `json:"date_updated,omitzero"`
	Description         string        `json:"description,omitzero"`
	MarkdownDescription string        `json:"markdown_description,omitzero"`
	URL                 string        `json:"url,omitzero"`
	List                Ref           `json:"list"`               // the home list
	Locations           []Ref         `json:"locations,omitzero"` // other lists it's also in
	CustomFields        []CustomField `json:"custom_fields,omitzero"`
	TimeSpent           FlexString    `json:"time_spent,omitzero"` // ms tracked, when time tracking is on
	Subtasks            []Task        `json:"subtasks,omitzero"`

	// Pending marks an optimistic task that the server hasn't confirmed yet.
	Pending bool `json:"-"`
}

// Label is the id users recognise: the custom id when the workspace has them.
func (t *Task) Label() string {
	if t.CustomID != "" {
		return t.CustomID
	}
	return t.ID
}

// Body is the task description, preferring markdown.
func (t *Task) Body() string {
	if t.MarkdownDescription != "" {
		return t.MarkdownDescription
	}
	return t.Description
}

// Field returns the custom field with the given id.
func (t *Task) Field(id string) (*CustomField, bool) {
	for i := range t.CustomFields {
		if t.CustomFields[i].ID == id {
			return &t.CustomFields[i], true
		}
	}
	return nil, false
}

type Comment struct {
	ID          FlexString    `json:"id"`
	CommentText string        `json:"comment_text"` // mentions come last here, see Text
	Parts       []CommentPart `json:"comment,omitzero"`
	User        User          `json:"user"`
	ReplyCount  FlexString    `json:"reply_count,omitzero"`
	Replies     []Comment     `json:"replies,omitzero"` // fetched separately, see Client.Comments
	Date        FlexString    `json:"date"`
	Pending     bool          `json:"-"`
}

// CommentPart is one piece of a rich comment: text, or a mention (Type "tag") of a user.
type CommentPart struct {
	Text string       `json:"text,omitempty"`
	Type string       `json:"type,omitempty"`
	User *CommentUser `json:"user,omitempty"`
	// Attributes is the formatting (ClickUp's editor is Quill): bold, italic, color,
	// background, code, link on text; code-block, list, header, blockquote on a "\n".
	Attributes map[string]any `json:"attributes,omitempty"`
}

type CommentUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

// Formatted reports whether the comment carries formatting beyond plain text and mentions.
func (c Comment) Formatted() bool {
	return slices.ContainsFunc(c.Parts, func(p CommentPart) bool { return len(p.Attributes) > 0 })
}

// Text is the comment as written. ClickUp's comment_text moves @mentions to the end, so a
// comment with mentions is rebuilt from its parts, which keep them in place. Right after
// posting, ClickUp may not have resolved a mention yet (a bare {"type":"tag"}): then the
// comment_text is all there is.
func (c Comment) Text() string {
	if !slices.ContainsFunc(c.Parts, func(p CommentPart) bool { return p.Type == "tag" }) ||
		slices.ContainsFunc(c.Parts, func(p CommentPart) bool { return p.Type == "tag" && p.Text == "" && p.User == nil }) {
		return c.CommentText
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p.Type == "tag" && p.Text == "" && p.User != nil {
			p.Text = "@" + p.User.Username
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

type List struct {
	ID       FlexString `json:"id"`
	Name     string     `json:"name"`
	Statuses []Status   `json:"statuses,omitzero"`
}

// Hierarchy types: a slimmed-down workspace tree.

type ListRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Folder struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Lists []ListRef `json:"lists"`
}

type Space struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Folders []Folder  `json:"folders"`
	Lists   []ListRef `json:"lists"`
}

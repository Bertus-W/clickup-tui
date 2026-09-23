// Package clickup is a small client for the ClickUp REST API v2.
package clickup

import (
	"bytes"
	"encoding/json"
	"strconv"
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
	ID         string    `json:"id"`
	Name       string    `json:"name,omitzero"`
	Label      string    `json:"label,omitzero"` // labels fields use "label" instead of "name"
	Color      string    `json:"color,omitzero"`
	OrderIndex FlexFloat `json:"orderindex"`
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
	List                Ref           `json:"list"`
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
	ID          FlexString `json:"id"`
	CommentText string     `json:"comment_text"`
	User        User       `json:"user"`
	Date        FlexString `json:"date"`
	Pending     bool       `json:"-"`
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

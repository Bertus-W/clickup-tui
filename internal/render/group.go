package render

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/style"
)

// GroupBy is how the task list is grouped, like the "Group by" of ClickUp's list view.
type GroupBy string

const (
	ByStatus   GroupBy = "status" // ClickUp's default: subtasks stay under their parent
	ByAssignee GroupBy = "assignee"
	ByPriority GroupBy = "priority"
	ByDue      GroupBy = "due"
)

var GroupNames = map[GroupBy]string{ByStatus: "Status", ByAssignee: "Assignee", ByPriority: "Priority", ByDue: "Due date"}

// Group is the heading a row is listed under.
type Group struct {
	Key   string
	Label string // may contain ANSI styling
	rank  []int  // sort order of the groups
}

// Grouped returns the tasks in display order for the grouping, each row with its group.
// Other groupings than status list subtasks as rows of their own: they may belong elsewhere.
func Grouped(tasks []*clickup.Task, by GroupBy, now time.Time) []Row {
	if by == "" || by == ByStatus {
		rows := Ordered(tasks)
		for i := range rows {
			if rows[i].Depth == 0 {
				rows[i].Group = statusGroup(rows[i].Task)
			} else {
				rows[i].Group = rows[i-1].Group // under the parent's heading
			}
		}
		return rows
	}
	rows := make([]Row, len(tasks))
	for i, t := range tasks {
		rows[i] = Row{Task: t, Group: groupOf(t, by, now)}
	}
	slices.SortStableFunc(rows, func(a, b Row) int {
		return cmp.Or(slices.Compare(a.Group.rank, b.Group.rank), cmp.Compare(a.Group.Key, b.Group.Key), compareWithin(a.Task, b.Task, by))
	})
	return rows
}

func compareWithin(a, b *clickup.Task, by GroupBy) int {
	if by == ByDue {
		if c := cmp.Compare(a.DueDate.Int(), b.DueDate.Int()); c != 0 {
			return c
		}
	}
	return compareTasks(a, b)
}

func statusGroup(t *clickup.Task) Group {
	name := strings.ToUpper(t.Status.Status)
	return Group{Key: "s:" + strings.ToLower(t.Status.Status), Label: style.Fg(t.Status.Color, style.Bold)("● " + name)}
}

func groupOf(t *clickup.Task, by GroupBy, now time.Time) Group {
	switch by {
	case ByAssignee:
		if len(t.Assignees) == 0 {
			return Group{Key: "~", Label: style.Dim("Unassigned"), rank: []int{1}}
		}
		u := t.Assignees[0]
		return Group{Key: strings.ToLower(u.Username), Label: style.Fg(u.Color, style.Bold)(u.Username), rank: []int{0}}
	case ByPriority:
		if t.Priority == nil {
			return Group{Key: "p:none", Label: style.Dim("No priority"), rank: []int{9}}
		}
		n := int(t.Priority.ID.Int())
		return Group{Key: fmt.Sprint("p:", n), Label: PriorityFlag(t, true), rank: []int{n}}
	case ByDue:
		due, ok := Millis(t.DueDate)
		if !ok {
			return Group{Key: "d:none", Label: style.Dim("No due date"), rank: []int{9}}
		}
		day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()) }
		days := int(day(due).Sub(day(now)).Hours() / 24)
		switch {
		case days < 0:
			return Group{Key: "d:0", Label: style.BoldRed("Overdue"), rank: []int{0}}
		case days == 0:
			return Group{Key: "d:1", Label: style.Yellow("Today"), rank: []int{1}}
		case days == 1:
			return Group{Key: "d:2", Label: style.Bold("Tomorrow"), rank: []int{2}}
		case days < 7:
			return Group{Key: "d:3", Label: style.Bold("Next 7 days"), rank: []int{3}}
		}
		return Group{Key: "d:4", Label: style.Bold("Later"), rank: []int{4}}
	}
	return statusGroup(t)
}

// Heading renders a group heading: its label and how many rows it holds.
func Heading(g Group, count int) string {
	return g.Label + style.Dim(fmt.Sprintf("  %d", count))
}

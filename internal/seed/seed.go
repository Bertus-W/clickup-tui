package seed

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/parse"
	"codeberg.org/b-wisman/clickup-tui/internal/render"
)

type Options struct {
	Space, List string
	Log         func(format string, args ...any)
}

// Run creates (or completes) the demo list in the given workspace. It is idempotent: the
// list is reused, existing tasks are skipped and only empty field values are filled in.
func Run(ctx context.Context, c *clickup.Client, teamID string, opts Options) error {
	log := opts.Log
	me, err := c.User(ctx)
	if err != nil {
		return err
	}
	spaces, err := c.Spaces(ctx, teamID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(spaces, func(s clickup.Space) bool { return s.Name == opts.Space })
	if i < 0 {
		names := make([]string, len(spaces))
		for i, s := range spaces {
			names[i] = s.Name
		}
		return fmt.Errorf("space %q not found; available: %s", opts.Space, strings.Join(names, ", "))
	}
	space := spaces[i]

	lists, err := c.FolderlessLists(ctx, space.ID)
	if err != nil {
		return err
	}
	var listID string
	if j := slices.IndexFunc(lists, func(l clickup.ListRef) bool { return l.Name == opts.List }); j >= 0 {
		listID = lists[j].ID
		log("list: reusing %q (%s)", opts.List, listID)
	} else {
		created, err := c.CreateList(ctx, space.ID, opts.List, "Demo data for clickup-tui")
		if err != nil {
			return err
		}
		listID = string(created.ID)
		log("list: created %q (%s)", opts.List, listID)
	}

	fields, err := ensureFields(ctx, c, listID, log)
	if err != nil {
		return err
	}
	list, err := c.GetList(ctx, listID)
	if err != nil {
		return err
	}

	existing := map[string]clickup.Task{}
	for page, err := range c.ListTasks(ctx, listID, true) {
		if err != nil {
			return err
		}
		for _, t := range page {
			existing[t.Name] = t
		}
	}

	for _, d := range Tasks {
		t, ok := existing[d.Name]
		if ok {
			for _, sub := range d.Subtasks { // an earlier run may have stopped halfway
				if s, found := existing[sub]; !found || string(s.Parent) != t.ID {
					if _, err := c.CreateTask(ctx, listID, map[string]any{"name": sub, "parent": t.ID}); err != nil {
						return err
					}
					log("task: added missing subtask %q", sub)
				}
			}
		}
		if !ok {
			body := map[string]any{"name": d.Name, "markdown_content": d.Description}
			if d.Priority > 0 {
				body["priority"] = d.Priority
			}
			if d.DueInDays != nil {
				body["due_date"] = parse.Noon(time.Now().AddDate(0, 0, *d.DueInDays))
				body["due_date_time"] = false
			}
			if slices.ContainsFunc(list.Statuses, func(s clickup.Status) bool { return s.Status == d.Status }) {
				body["status"] = d.Status
			}
			if d.Mine {
				body["assignees"] = []int64{me.ID}
			}
			if t, err = c.CreateTask(ctx, listID, body); err != nil {
				return err
			}
			for _, sub := range d.Subtasks {
				if _, err := c.CreateTask(ctx, listID, map[string]any{"name": sub, "parent": t.ID}); err != nil {
					return err
				}
			}
			log("task: created %q (+%d subtasks)", d.Name, len(d.Subtasks))
		}
		for name, want := range map[string]string{"Scope": d.Scope, "Severity": d.Severity} {
			field, ok := fields[name]
			if !ok {
				continue
			}
			if current, ok := t.Field(field.ID); ok && current.IsSet() {
				continue
			}
			for _, o := range render.Options(&field) {
				if o.Title() == want {
					if err := c.SetField(ctx, t.ID, field.ID, o.ID, nil); err != nil {
						return err
					}
				}
			}
		}
	}
	log("done: %d demo tasks with Scope and Severity", len(Tasks))
	return nil
}

func ensureFields(ctx context.Context, c *clickup.Client, listID string, log func(string, ...any)) (map[string]clickup.CustomField, error) {
	byName := func() (map[string]clickup.CustomField, error) {
		all, err := c.ListFields(ctx, listID)
		out := map[string]clickup.CustomField{}
		for _, f := range all {
			out[f.Name] = f
		}
		return out, err
	}
	have, err := byName()
	if err != nil {
		return nil, err
	}
	for _, f := range Fields {
		if _, ok := have[f.Name]; ok {
			continue
		}
		field := clickup.CustomField{Name: f.Name, Type: "drop_down"}
		for _, o := range f.Options {
			field.TypeConfig.Options = append(field.TypeConfig.Options, clickup.Option{Name: o.Name, Color: o.Color})
		}
		if err := c.CreateField(ctx, listID, field); err != nil {
			log("field: could not create %s via the API (%v); add it in ClickUp and re-run", f.Name, err)
			continue
		}
		log("field: created %s", f.Name)
	}
	return byName()
}

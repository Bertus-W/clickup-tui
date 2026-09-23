package app

import (
	"context"
	"os"
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/config"
)

// TestLiveMoveAndMention checks the two calls the fake can't vouch for against the real
// ClickUp API: moving a task (API v3) and a comment with an @mention. It runs only with
// CU_LIVE=1, uses your config, and works on a throwaway task in the "clickup-tui demo" list
// (cu seed makes it) that it deletes afterwards.
//
//	CU_LIVE=1 go test -run Live -v ./internal/app
func TestLiveMoveAndMention(t *testing.T) {
	if os.Getenv("CU_LIVE") == "" {
		t.Skip("set CU_LIVE=1 to run against your ClickUp workspace")
	}
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil || cfg.Token == "" {
		t.Fatal("no token configured: run cu setup")
	}
	api := clickup.New(cfg.Token)
	me, err := api.User(ctx)
	check(err)
	teamID := cfg.TeamID
	if teamID == "" {
		teams, err := api.Teams(ctx)
		check(err)
		teamID = string(teams[0].ID)
	}
	spaces, err := api.Hierarchy(ctx, teamID)
	check(err)
	var from, to clickup.ListRef
	for _, s := range spaces {
		lists := s.Lists
		for _, f := range s.Folders {
			lists = append(lists, f.Lists...)
		}
		for _, l := range lists {
			switch {
			case l.Name == "clickup-tui demo":
				from = l
			case to.ID == "":
				to = l
			}
		}
	}
	if from.ID == "" || to.ID == "" {
		t.Fatal(`needs the "clickup-tui demo" list (cu seed) and one other list`)
	}

	task, err := api.CreateTask(ctx, from.ID, map[string]any{"name": "cu live check (throwaway, safe to delete)"})
	check(err)
	defer func() {
		if err := api.DeleteTask(ctx, task.ID); err != nil {
			t.Errorf("cleanup failed, delete %s by hand: %v", task.ID, err)
		}
	}()

	check(api.MoveTask(ctx, teamID, task.ID, to.ID))
	moved, err := api.GetTask(ctx, task.ID, "")
	check(err)
	if string(moved.List.ID) != to.ID {
		t.Errorf("after the move the task is in %q, want %q", moved.List.Name, to.Name)
	}
	check(api.MoveTask(ctx, teamID, task.ID, from.ID))

	text := "live check: hi @" + me.Username + " (throwaway)"
	parts, _ := mentionParts(text, []clickup.User{me})
	check(api.CreateComment(ctx, task.ID, text, parts))
	comments, err := api.Comments(ctx, task.ID)
	check(err)
	if len(comments) != 1 || comments[0].Text() != text {
		t.Errorf("comment reads %q, want %q", comments[0].Text(), text)
	}
}

// Package setup asks for the API token and workspace on the first launch and saves them.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/config"
)

// ErrCancelled means the user gave no token.
var ErrCancelled = errors.New("setup cancelled: no token given")

// Terminal is how setup talks to the user.
type Terminal struct {
	In     *bufio.Reader
	Out    io.Writer
	Secret func() (string, error) // reads a line without echoing it
}

func (t Terminal) line(prompt string) (string, error) {
	fmt.Fprint(t.Out, prompt)
	s, err := t.In.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (t Terminal) secret(prompt string) (string, error) {
	fmt.Fprint(t.Out, prompt)
	s, err := t.Secret()
	fmt.Fprintln(t.Out)
	return strings.TrimSpace(s), err
}

// Run asks for a personal API token until one works, lets the user pick a workspace and
// saves both to the config file. newClient makes an API client for a token.
func Run(ctx context.Context, term Terminal, newClient func(token string) *clickup.Client) (config.Config, error) {
	out := term.Out
	fmt.Fprintf(out, "\nWelcome to cu, a fast terminal UI for ClickUp.\n\n")
	fmt.Fprintf(out, "cu needs your personal ClickUp API token. Get it in ClickUp: click your avatar,\n")
	fmt.Fprintf(out, "then Settings → Apps → API Token (Generate/Copy). It starts with pk_.\n")
	fmt.Fprintf(out, "It will be saved in %s, readable only by you.\n\n", config.File())

	for {
		token, err := term.secret("API token (input hidden, empty to cancel): ")
		if err != nil {
			return config.Config{}, err
		}
		if token == "" {
			return config.Config{}, ErrCancelled
		}
		if !strings.HasPrefix(token, "pk_") {
			fmt.Fprintf(out, "That doesn't look like a personal API token: those start with pk_. Try again.\n\n")
			continue
		}
		fmt.Fprintf(out, "Checking the token with ClickUp…\n")
		client := newClient(token)
		me, err := client.User(ctx)
		if err != nil {
			fmt.Fprintf(out, "ClickUp didn't accept that token (%v). Try again.\n\n", err)
			continue
		}
		teams, err := client.Teams(ctx)
		if err != nil {
			return config.Config{}, fmt.Errorf("loading your workspaces: %w", err)
		}
		if len(teams) == 0 {
			return config.Config{}, errors.New("this token has no workspaces")
		}
		fmt.Fprintf(out, "Hi %s!\n\n", me.Username)

		team, err := pickTeam(term, teams)
		if err != nil {
			return config.Config{}, err
		}
		cfg := config.Config{Token: token, TeamID: string(team.ID)}
		if err := config.Save(cfg); err != nil {
			return config.Config{}, fmt.Errorf("saving %s: %w", config.File(), err)
		}
		fmt.Fprintf(out, "Saved to %s. Opening %s…\n", config.File(), team.Name)
		return cfg, nil
	}
}

// pickTeam lets the user choose a workspace by number or team id; one workspace needs no question.
func pickTeam(term Terminal, teams []clickup.Team) (clickup.Team, error) {
	if len(teams) == 1 {
		fmt.Fprintf(term.Out, "Using your workspace %s (team id %s).\n", teams[0].Name, teams[0].ID)
		return teams[0], nil
	}
	fmt.Fprintf(term.Out, "Your workspaces:\n")
	for i, t := range teams {
		fmt.Fprintf(term.Out, "  %d. %s (team id %s)\n", i+1, t.Name, t.ID)
	}
	for {
		answer, err := term.line(fmt.Sprintf("Which one should cu open? [1-%d, or a team id; enter for 1]: ", len(teams)))
		if err != nil {
			return clickup.Team{}, err
		}
		if answer == "" {
			return teams[0], nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(teams) {
			return teams[n-1], nil
		}
		for _, t := range teams {
			if string(t.ID) == answer {
				return t, nil
			}
		}
		fmt.Fprintf(term.Out, "No workspace %q. Pick a number from the list.\n", answer)
	}
}

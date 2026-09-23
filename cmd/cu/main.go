// Command cu is a fast, lazygit-style terminal UI for ClickUp.
//
//	cu              open the TUI (the first time, it asks for your API token and workspace)
//	cu setup        ask for the API token and workspace again
//	cu demo         try it offline against a built-in demo project
//	cu seed         create the demo project in your workspace (idempotent)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/term"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/config"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
	"codeberg.org/b-wisman/clickup-tui/internal/gui"
	"codeberg.org/b-wisman/clickup-tui/internal/seed"
	"codeberg.org/b-wisman/clickup-tui/internal/setup"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cu:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		c, err := cache.Open(cfg.CachePath)
		if err != nil {
			return err
		}
		defer c.Close()
		return runTUI(clickup.New(cfg.Token), c, cfg.TeamID)
	case "demo":
		c, err := cache.Open(":memory:")
		if err != nil {
			return err
		}
		defer c.Close()
		return runTUI(fake.Demo().Client(), c, "")
	case "setup":
		if _, err := runSetup(); err != nil {
			return err
		}
		return run(nil)
	case "seed":
		return runSeed(args)
	case "-h", "--help", "help":
		fmt.Println("usage: cu [setup | demo | seed [-space NAME] [-list NAME]]")
		return nil
	}
	return fmt.Errorf("unknown command %q (try cu help)", cmd)
}

// loadConfig loads the config, running the first-launch setup when there is no token yet.
func loadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, fmt.Errorf("reading %s: %w", config.File(), err)
	}
	if cfg.Token != "" {
		return cfg, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return cfg, fmt.Errorf("no ClickUp API token found. Run cu in a terminal to set it up, "+
			"export CLICKUP_API_TOKEN=pk_..., or put `token = \"pk_...\"` in %s", config.File())
	}
	saved, err := runSetup()
	if err != nil {
		return cfg, err
	}
	cfg.Token, cfg.TeamID = saved.Token, saved.TeamID
	return cfg, nil
}

// runSetup asks for the token (hidden) and workspace in the terminal and saves them.
func runSetup() (config.Config, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return setup.Run(ctx, setup.Terminal{
		In:     bufio.NewReader(os.Stdin),
		Out:    os.Stdout,
		Secret: func() (string, error) { b, err := term.ReadPassword(int(os.Stdin.Fd())); return string(b), err },
	}, clickup.New)
}

func runTUI(api *clickup.Client, c *cache.Cache, teamPref string) error {
	g, err := gui.New(gui.Options{})
	if err != nil {
		return err
	}
	a := app.New(api, c, g, g.Async())
	a.TeamPref = teamPref
	g.Attach(a)
	return g.Run()
}

func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	space := fs.String("space", "Team Space", "space to create the demo list in")
	list := fs.String("list", "clickup-tui demo", "name of the demo list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	api := clickup.New(cfg.Token)
	teamID := cfg.TeamID
	if teamID == "" {
		teams, err := api.Teams(ctx)
		if err != nil {
			return err
		}
		if len(teams) == 0 {
			return fmt.Errorf("no workspaces for this token")
		}
		teamID = string(teams[0].ID)
	}
	return seed.Run(ctx, api, teamID, seed.Options{
		Space: *space, List: *list,
		Log: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
}

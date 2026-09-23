// Command cu is a fast, lazygit-style terminal UI for ClickUp.
//
//	cu              open the TUI
//	cu demo         try it offline against a built-in demo project
//	cu seed         create the demo project in your workspace (idempotent)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"codeberg.org/b-wisman/clickup-tui/internal/app"
	"codeberg.org/b-wisman/clickup-tui/internal/cache"
	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/config"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
	"codeberg.org/b-wisman/clickup-tui/internal/gui"
	"codeberg.org/b-wisman/clickup-tui/internal/seed"
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
	case "seed":
		return runSeed(args)
	case "-h", "--help", "help":
		fmt.Println("usage: cu [demo | seed [-space NAME] [-list NAME]]")
		return nil
	}
	return fmt.Errorf("unknown command %q (try cu help)", cmd)
}

func loadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, err
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("no ClickUp API token found.\n"+
			"Create one under ClickUp → Settings → Apps → API Token, then either:\n"+
			"  export CLICKUP_API_TOKEN=pk_...\n"+
			"or put `token = \"pk_...\"` in %s", config.File())
	}
	return cfg, nil
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

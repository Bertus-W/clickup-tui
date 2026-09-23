// Package config loads the API token and paths.
//
// The token comes from CLICKUP_API_TOKEN or ~/.config/clickup-tui/config.toml:
//
//	token = "pk_..."
//	team_id = "1234567"   # optional, defaults to the first workspace
package config

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Token     string `toml:"token"`
	TeamID    string `toml:"team_id"`
	CachePath string `toml:"-"`
}

func Dir() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "clickup-tui")
}

func File() string { return filepath.Join(Dir(), "config.toml") }

func xdg(env, fallback string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func Load() (Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(File(), &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	cfg.Token = cmp.Or(os.Getenv("CLICKUP_API_TOKEN"), cfg.Token)
	cfg.TeamID = cmp.Or(os.Getenv("CLICKUP_TEAM_ID"), cfg.TeamID)
	cfg.CachePath = filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "clickup-tui", "cache-go.db")
	return cfg, nil
}

// Package config loads the API token and paths.
//
// The token comes from CLICKUP_API_TOKEN or ~/.config/clickup-tui/config.toml:
//
//	token = "pk_..."
//	team_id = "1234567"   # optional, defaults to the first workspace
package config

import (
	"bytes"
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Token     string `toml:"token"`
	TeamID    string `toml:"team_id,omitempty"`
	CachePath string `toml:"-"`
}

// Dir is the config directory: $XDG_CONFIG_HOME/clickup-tui, ~/.config/clickup-tui, or on
// Windows %AppData%\clickup-tui.
func Dir() string {
	return filepath.Join(base("XDG_CONFIG_HOME", ".config", os.UserConfigDir), "clickup-tui")
}

func File() string { return filepath.Join(Dir(), "config.toml") }

// base prefers the XDG variable, then the platform directory on Windows, then ~/fallback
// (also on macOS, where terminal tools conventionally use ~/.config and ~/.cache).
func base(env, fallback string, windows func() (string, error)) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	if runtime.GOOS == "windows" {
		if dir, err := windows(); err == nil {
			return dir
		}
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
	cfg.CachePath = filepath.Join(base("XDG_CACHE_HOME", ".cache", os.UserCacheDir), "clickup-tui", "cache-go.db")
	return cfg, nil
}

// Save writes the token and workspace to the config file, creating its folder first. The
// file holds a secret, so only the user can read it.
func Save(cfg Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# clickup-tui: https://codeberg.org/b-wisman/clickup-tui\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	// Write a temporary file and rename it, so a crash never leaves half a config.
	tmp, err := os.CreateTemp(Dir(), "config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), File())
}

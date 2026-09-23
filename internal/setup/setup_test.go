package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"codeberg.org/b-wisman/clickup-tui/internal/clickup"
	"codeberg.org/b-wisman/clickup-tui/internal/config"
	"codeberg.org/b-wisman/clickup-tui/internal/fake"
)

func run(t *testing.T, secrets []string, lines string) (config.Config, string, error) {
	t.Helper()
	var out strings.Builder
	srv := fake.Basic(1)
	term := Terminal{
		In:  bufio.NewReader(strings.NewReader(lines)),
		Out: &out,
		Secret: func() (string, error) {
			s := secrets[0]
			secrets = secrets[1:]
			return s, nil
		},
	}
	newClient := func(token string) *clickup.Client {
		c := srv.Client()
		if token != "pk_good" {
			c.BaseURL = "http://clickup.fake/api/v2/unknown" // the fake answers 404: a rejected token
		}
		return c
	}
	cfg, err := Run(t.Context(), term, newClient)
	return cfg, out.String(), err
}

// The first launch asks until a token works, then saves it in a folder it creates.
func TestFirstLaunchSavesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "does", "not", "exist"))
	cfg, out, err := run(t, []string{"not-a-token", "pk_bad", "pk_good"}, "")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{"start with pk_", "didn't accept that token", "Hi tester!", "Using your workspace Acme", "Saved to"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if cfg.Token != "pk_good" || cfg.TeamID != fake.TeamID {
		t.Fatalf("cfg = %+v", cfg)
	}
	info, err := os.Stat(config.File())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", info.Mode().Perm())
	}
	loaded, err := config.Load()
	if err != nil || loaded.Token != "pk_good" || loaded.TeamID != fake.TeamID {
		t.Fatalf("reloaded = %+v, %v", loaded, err)
	}
}

func TestEmptyTokenCancels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := run(t, []string{""}, ""); err != ErrCancelled {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(config.File()); err == nil {
		t.Fatal("a cancelled setup wrote a config")
	}
}

func TestPickTeam(t *testing.T) {
	teams := []clickup.Team{{ID: "1", Name: "Personal"}, {ID: "90", Name: "Work"}}
	for answer, want := range map[string]string{"\n": "1", "2\n": "90", "90\n": "90", "7\nx\n2\n": "90"} {
		var out strings.Builder
		got, err := pickTeam(Terminal{In: bufio.NewReader(strings.NewReader(answer)), Out: &out}, teams)
		if err != nil || string(got.ID) != want {
			t.Errorf("answer %q: got %v, %v; want %s", answer, got.ID, err, want)
		}
	}
}

package gui

import (
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// runEditor opens text in $VISUAL/$EDITOR while the TUI is suspended, like lazygit's `e`.
// ok is false when the editor failed or couldn't run.
func (gui *Gui) runEditor(initial string) (string, bool, error) {
	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	f, err := os.CreateTemp("", "clickup-*.md")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(initial); err != nil {
		f.Close()
		return "", false, err
	}
	f.Close()

	if err := gui.g.Suspend(); err != nil {
		return "", false, err
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	if err := gui.g.Resume(); err != nil {
		return "", false, err
	}
	if runErr != nil {
		return "", false, fmt.Errorf("%s: %w", editor, runErr)
	}
	data, err := os.ReadFile(f.Name())
	return string(data), err == nil, err
}

// copyToClipboard uses the platform tool, falling back to the OSC 52 escape sequence.
func copyToClipboard(text string) error {
	for _, args := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}} {
		if path, err := exec.LookPath(args[0]); err == nil {
			cmd := exec.Command(path, args[1:]...)
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return errors.New("no clipboard tool found")
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}

func openURL(url string) error {
	name := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command(name, url).Start()
}

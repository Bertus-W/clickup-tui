package gui

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/atotto/clipboard"
)

// editorCommand is $VISUAL or $EDITOR (which may carry arguments, e.g. "code --wait"),
// defaulting to vi, or Notepad on Windows.
func editorCommand() []string {
	fallback := "vi"
	if runtime.GOOS == "windows" {
		fallback = "notepad"
	}
	return strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), fallback))
}

// runEditor opens text in the editor while the TUI is suspended, like lazygit's `e`.
// ok is false when the editor failed or couldn't run.
func (gui *Gui) runEditor(initial string) (string, bool, error) {
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

	editor := editorCommand()
	if err := gui.g.Suspend(); err != nil {
		return "", false, err
	}
	// Run the editor directly (no shell), so it works the same on Windows.
	cmd := exec.Command(editor[0], append(editor[1:], f.Name())...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	if err := gui.g.Resume(); err != nil {
		return "", false, err
	}
	if runErr != nil {
		return "", false, fmt.Errorf("%s: %w", strings.Join(editor, " "), runErr)
	}
	data, err := os.ReadFile(f.Name())
	return string(data), err == nil, err
}

// copyToClipboard uses the system clipboard (Windows, macOS, X11 and Wayland), falling
// back to the OSC 52 escape sequence, which most terminals turn into a copy.
func copyToClipboard(text string) error {
	if !clipboard.Unsupported {
		if err := clipboard.WriteAll(text); err == nil {
			return nil
		}
	}
	var out io.Writer = os.Stdout
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		out = tty
	}
	_, err := fmt.Fprintf(out, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}

func openURL(url string) error {
	cmd := exec.Command("xdg-open", url)
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it, so no zombie process is left behind
	return nil
}

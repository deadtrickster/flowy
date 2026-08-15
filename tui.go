package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deadtrickster/flowy/internal/tui"
)

// `flowy tui` - the terminal client.
//
// It is a client of the HTTP API and nothing else: no database handle, no
// store, no second permission filter. That is what makes it safe to run
// somewhere the node is not - the usual case being a person over ssh inside
// tmux, pointed at a `flowy serve` on another box - and it is why a bad token
// here is an ordinary 401 rather than a process that got halfway into somebody
// else's data before finding out.

// defaultTUIAddr is where a node listens when nothing says otherwise.
const defaultTUIAddr = "http://127.0.0.1:8787"

// tokenFile is where a person's token lives when it is not in the environment.
// It is read as the third choice, after the flag and after FLOWY_TOKEN, so a
// one-off against another node does not mean editing a file.
const tokenFile = "flowy/token"

func tuiCmd(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	url := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/"+tokenFile+")")
	if err := fs.Parse(args); err != nil {
		return err
	}

	base := resolveURL(*url, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errors.New("no token: pass --token, set FLOWY_TOKEN, or write one to " +
			"~/.config/" + tokenFile + " - every read this makes is a read the node " +
			"has to attribute to somebody")
	}

	client := tui.NewClient(base, bearer)
	model := tui.New(client, tui.EnvTheme())

	// The alt screen is what makes this tmux-friendly: the pane's scrollback is
	// untouched while the client runs and comes back when it exits. No mouse is
	// asked for, so selecting text with the terminal's own selection still
	// works, and copy-mode is not fighting anybody for the wheel.
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return err
	}
	return nil
}

// resolveURL is the flag, then FLOWY_ADDR, then the default. A FLOWY_ADDR of
// the sort `flowy serve` takes - a bare host:port, or :8787 - is turned into a
// URL rather than refused, because that is the value people already have in
// their environment for the server.
func resolveURL(flagValue, env string) string {
	value := strings.TrimSpace(flagValue)
	if value == "" {
		value = strings.TrimSpace(env)
	}
	if value == "" {
		return defaultTUIAddr
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	if strings.HasPrefix(value, ":") {
		return "http://127.0.0.1" + value
	}
	return "http://" + value
}

// resolveToken is the flag, then FLOWY_TOKEN, then ~/.config/flowy/token. A
// file that is not there is not an error - it is the ordinary case on a machine
// where the token is in the environment - but a file that is there and cannot
// be read is, because silently falling through to "no token" would report the
// permissions problem as a missing configuration.
func resolveToken(flagValue, env string) (string, error) {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(env); value != "" {
		return value, nil
	}
	path, err := tokenPath()
	if err != nil {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// tokenPath is $XDG_CONFIG_HOME/flowy/token, or ~/.config/flowy/token.
func tokenPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, tokenFile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", tokenFile), nil
}

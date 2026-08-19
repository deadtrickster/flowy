package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
// It is read last, after the flag, after a named principal and after
// FLOWY_TOKEN, so a one-off against another node does not mean editing a file.
//
// It holds the OPERATOR'S OWN token, which is why reaching it is the one
// resolution that says so out loud - see resolveToken.
const tokenFile = "flowy/token"

// agentTokenDir is where a seat's own token lives, exactly one path segment
// below the person's: ~/.config/flowy/agents/<name>. The safe credential and
// the dangerous one being neighbours is the whole reason this file has to be
// able to tell them apart out loud.
const agentTokenDir = "flowy/agents"

// operatorSelf is the name a person uses to say that the personal token is the
// one they meant. It is not a seat and never reads the agents directory: it is
// the operator answering the warning below, once, instead of being asked again
// on every command.
const operatorSelf = "me"

// tokenWarnings is where the loud line about an unnamed principal goes. A
// variable rather than a bare os.Stderr so a test can read what a resolution
// said, because "it warns" is only a property worth having if it can be checked
// - a warning nobody proves is printed is the same silence this fixes.
var tokenWarnings io.Writer = os.Stderr

// agentFlagHelp is the one description of --agent, so the five commands that
// take it describe it identically.
const agentFlagHelp = "the seat speaking, whose token is read from ~/.config/" +
	agentTokenDir + "/<name> (default $FLOWY_AGENT; " + operatorSelf +
	" means the operator's own token)"

func tuiCmd(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	url := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/"+tokenFile+")")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}

	base := resolveURL(*url, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
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

// resolveToken decides which principal a command speaks as, in this order:
//
//	--token          the credential typed at the moment of the call
//	--agent NAME     ~/.config/flowy/agents/NAME
//	FLOWY_AGENT      the same, from the environment
//	FLOWY_TOKEN      a credential from the environment
//	~/.config/flowy/token, and it says so on stderr
//
// A named principal outranks a bare credential on purpose. FLOWY_TOKEN is very
// often something a harness exported once and forgot; FLOWY_AGENT is a
// statement about who is acting now, made by whoever started this process. When
// the two disagree the statement wins, because the failure this ordering exists
// to prevent is a seat that knows its own name still posting as somebody else.
//
// The last line is the bug. ~/.config/flowy/token is the OPERATOR'S personal
// token, so any agent that shelled out to the CLI without a token of its own
// spoke as the operator - and nothing anywhere said a word, so it was found
// only when the operator noticed they could not see their own messages. It
// still works, because taking it away today would break the operator's own
// shell and every script already written against it, but it can no longer
// happen quietly: reaching it prints a warning naming the file, the seat's
// directory one segment over, and how to make the warning stop.
//
// A file that is not there is not an error - it is the ordinary case on a
// machine where the token is in the environment - but a file that is there and
// cannot be read is, because silently falling through to "no token" would
// report the permissions problem as a missing configuration.
//
// Naming a principal that does not resolve is always a refusal and never a
// fallback. Downgrading a named seat to the operator's token is precisely the
// accident, and it would be at its most likely exactly when a seat is new or
// its file is misspelt.
func resolveToken(flagToken, envToken, flagAgent, envAgent string) (string, error) {
	if value := strings.TrimSpace(flagToken); value != "" {
		if name := strings.TrimSpace(flagAgent); name != "" {
			return "", fmt.Errorf("two principals named: --token carries one credential "+
				"and --agent %s names another - pass one", name)
		}
		return value, nil
	}
	// The flag first, then the environment, so a one-off `--agent x` overrides
	// the seat the surrounding process was started as rather than being
	// silently ignored by it.
	name := strings.TrimSpace(flagAgent)
	source := "--agent"
	if name == "" {
		name, source = strings.TrimSpace(envAgent), "FLOWY_AGENT"
	}
	if name != "" {
		seat, err := agentToken(name, source)
		if err != nil {
			return "", err
		}
		// AN EXPLICIT CREDENTIAL LOSING TO AN AMBIENT NAME, SAID OUT LOUD.
		//
		// One line up, the same collision is a refusal: --token with --agent
		// answers "two principals named ... pass one". Here the agent simply
		// wins, and until this warning it won silently. FLOWY_TOKEN is a
		// credential somebody chose; FLOWY_AGENT is usually INHERITED - a
		// drainer exports one, a wrapper sets one, a shell has one left over -
		// so the ambient value outranked the deliberate one and the write went
		// out attributed to a seat the caller never named.
		//
		// A warning rather than a refusal, because a flag pair is two things
		// typed in one breath while an environment pair is two things set at
		// different times by different people.
		//
		// TWO NAMES FOR ONE PRINCIPAL IS NOT A COLLISION, and the first live
		// output of the previous version was this exact false alarm about a
		// correct call. scripts/say.sh passes FLOWY_TOKEN=<the named seat's own
		// token> and FLOWY_AGENT names that same seat, so every message every
		// agent sent printed four lines about a conflict that did not exist -
		// which is the "a warning printed unconditionally is the same as no
		// warning" failure, arriving through the deployment rather than through
		// the logic. The test proved the logic.
		//
		// So the question is whether the credential being set aside is a
		// DIFFERENT one, which is the thing worth saying.
		if source == "FLOWY_AGENT" {
			if explicit := strings.TrimSpace(envToken); explicit != "" && explicit != seat {
				warnTokenLostToAgent(name)
			}
		}
		return seat, nil
	}
	if value := strings.TrimSpace(envToken); value != "" {
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
	value := strings.TrimSpace(string(raw))
	if value != "" {
		warnUnnamedPrincipal(path)
	}
	return value, nil
}

// agentToken reads the token of a named principal. The name reserved for the
// operator returns the personal token without the warning, which is the point
// of it: the person at the keyboard gets to answer the question once rather
// than be asked it on every command.
func agentToken(name, source string) (string, error) {
	if err := checkPrincipalName(name, source); err != nil {
		return "", err
	}
	dir, err := agentTokenDirPath()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", source, name, err)
	}
	if name == operatorSelf {
		// An actual seat called "me" would be read as the operator by everything
		// here, so say that rather than quietly preferring one reading of it.
		if _, err := os.Stat(filepath.Join(dir, operatorSelf)); err == nil {
			return "", fmt.Errorf("%s is reserved for the operator's own token, but a seat "+
				"of that name exists at %s - rename the seat",
				operatorSelf, filepath.Join(dir, operatorSelf))
		}
		path, err := tokenPath()
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return "", fmt.Errorf("%s is empty, so %s %s names nobody", path, source, name)
		}
		return value, nil
	}
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s %s names a principal with no token: %s does not exist. "+
			"Mint one for that seat, or pass --token. This does not fall back to the "+
			"operator's own token in %s - speaking as somebody else is worse than not "+
			"speaking", source, name, path, "~/.config/"+tokenFile)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("%s is empty, so %s %s names nobody", path, source, name)
	}
	return value, nil
}

// checkPrincipalName refuses anything that is not one path segment. The name
// becomes a filename, so a slash or a .. in it reads a token the caller did not
// mean to name - and the one credential most worth reaching that way is sitting
// in the parent directory.
func checkPrincipalName(name, source string) error {
	if name != filepath.Base(name) || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%s %q is not a name: a principal is one path segment, "+
			"not a path", source, name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%s %q is not a name: it cannot start with a dot", source, name)
	}
	return nil
}

// warnUnnamedPrincipal is the loud half of the fix. It goes to stderr, where
// every other line a person reads goes, so stdout stays parseable for the
// scripts that already pipe it.
func warnUnnamedPrincipal(path string) {
	agents := "~/.config/" + agentTokenDir + "/<name>"
	if dir, err := agentTokenDirPath(); err == nil {
		agents = filepath.Join(dir, "<name>")
	}
	fmt.Fprintf(tokenWarnings,
		"flowy: WARNING: nothing named who is speaking, so this is the operator's own\n"+
			"flowy: token from %s.\n"+
			"flowy: If you are an agent, this posts AS THE OPERATOR. Set FLOWY_AGENT=<name>\n"+
			"flowy: or pass --agent <name>, and your token is read from %s.\n"+
			"flowy: If you are the operator, say so with --agent %s and this stops.\n",
		path, agents, operatorSelf)
}

// warnTokenLostToAgent says that an explicit credential was set aside for an
// inherited name.
//
// It names the winner rather than describing the rule, because the reader is
// about to write something and what they need is "this will be attributed to
// claude-host" - not a paragraph about precedence they then have to apply.
func warnTokenLostToAgent(name string) {
	fmt.Fprintf(tokenWarnings,
		"flowy: WARNING: FLOWY_TOKEN is set and FLOWY_AGENT=%s wins - this speaks as\n"+
			"flowy: %s, not as whoever that token belongs to.\n"+
			"flowy: To use the token, unset FLOWY_AGENT. To silence this, pass --agent %s\n"+
			"flowy: and mean it.\n",
		name, name, name)
}

// tokenPath is $XDG_CONFIG_HOME/flowy/token, or ~/.config/flowy/token.
func tokenPath() (string, error) {
	return configPath(tokenFile)
}

// agentTokenDirPath is $XDG_CONFIG_HOME/flowy/agents, or ~/.config/flowy/agents.
func agentTokenDirPath() (string, error) {
	return configPath(agentTokenDir)
}

// configPath resolves one entry under the config directory, honouring
// XDG_CONFIG_HOME so that a test - and a person with a non-default config home
// - reaches the same place for the person's token and for the seats' tokens.
func configPath(rel string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, rel), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", rel), nil
}

// errNoToken is what every client command says when nothing anywhere named a
// principal. One text because it is one situation: five slightly different
// versions of it taught five slightly different fixes.
func errNoToken() error {
	return errors.New("no token: pass --token, name a principal with --agent NAME or " +
		"FLOWY_AGENT, set FLOWY_TOKEN, or write one to ~/.config/" + tokenFile +
		" - every request this makes is one the node has to attribute to somebody")
}

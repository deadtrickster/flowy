package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The look, and what happens to it on a terminal that cannot do it.
//
// There is no assumption here that the terminal is xterm-256color on a laptop.
// The whole point of this client is that it runs over ssh inside tmux, and the
// terminals it lands on range from truecolor to a 16-colour linux console to a
// TERM=dumb that has nothing. lipgloss degrades a colour to the nearest one the
// detected profile has, so the styles below are written once in truecolor and
// come out as the closest 16-colour approximation, or as no colour at all,
// without a second set of definitions.
//
// The one thing lipgloss will not do is stop drawing box characters, so that is
// what Unicode below is for.

// Theme is the resolved appearance: the colour profile that was detected, and
// whether the terminal can be trusted with anything above ASCII.
type Theme struct {
	Profile termenv.Profile
	Unicode bool

	Tab       lipgloss.Style
	TabActive lipgloss.Style
	Status    lipgloss.Style
	Help      lipgloss.Style
	Title     lipgloss.Style
	Label     lipgloss.Style
	Dim       lipgloss.Style
	Selected  lipgloss.Style
	Agent     lipgloss.Style
	Human     lipgloss.Style
	Err       lipgloss.Style
	OK        lipgloss.Style
	Badge     lipgloss.Style
	Bar       lipgloss.Style

	// The three states a todo is in. They are their own styles rather than
	// Dim/OK/Badge for the reason the console gives them their own colours: a
	// status is a fact about the work, not a role in the interface, and OK
	// already means "this went well" on half a dozen other rows.
	TodoActive lipgloss.Style
	TodoOpen   lipgloss.Style
	TodoDone   lipgloss.Style

	Severity map[string]lipgloss.Style
}

// ProfileName is what the status line calls the detected profile. It is worth
// showing: "the colours are wrong" and "this terminal reports itself as having
// no colours" are the same symptom and different problems.
func (t Theme) ProfileName() string {
	switch t.Profile {
	case termenv.TrueColor:
		return "truecolor"
	case termenv.ANSI256:
		return "256"
	case termenv.ANSI:
		return "16"
	default:
		return "mono"
	}
}

// DetectProfile reads the environment the way every other terminal program
// does: NO_COLOR wins over everything, TERM=dumb or an unset TERM means a
// terminal that cannot be assumed to do anything, and otherwise whatever
// termenv worked out from TERM and COLORTERM stands.
func DetectProfile(env func(string) string, detected termenv.Profile) termenv.Profile {
	if env("NO_COLOR") != "" {
		return termenv.Ascii
	}
	term := env("TERM")
	if term == "" || term == "dumb" {
		return termenv.Ascii
	}
	if strings.Contains(env("COLORTERM"), "truecolor") || env("COLORTERM") == "24bit" {
		return termenv.TrueColor
	}
	return detected
}

// unicodeOK reports whether box-drawing and block characters are safe. A locale
// that says nothing about UTF-8 gets ASCII, because a sparkline made of
// question marks is worse than one made of hashes.
func unicodeOK(env func(string) string) bool {
	if env("TERM") == "dumb" || env("TERM") == "" {
		return false
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := env(name); value != "" {
			return strings.Contains(strings.ToUpper(value), "UTF-8") ||
				strings.Contains(strings.ToUpper(value), "UTF8")
		}
	}
	return false
}

// NewTheme builds the styles for a profile.
func NewTheme(profile termenv.Profile, unicode bool) Theme {
	// A renderer pinned to the profile, so that a test can ask for mono and get
	// mono whether or not the process happens to be attached to a terminal.
	renderer := lipgloss.NewRenderer(os.Stdout, termenv.WithProfile(profile))
	renderer.SetColorProfile(profile)
	style := func() lipgloss.Style { return renderer.NewStyle() }

	t := Theme{Profile: profile, Unicode: unicode}
	t.Tab = style().Foreground(lipgloss.Color("245"))
	t.TabActive = style().Bold(true).Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("39"))
	t.Status = style().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236"))
	t.Help = style().Foreground(lipgloss.Color("244"))
	t.Title = style().Bold(true).Foreground(lipgloss.Color("81"))
	t.Label = style().Foreground(lipgloss.Color("109"))
	t.Dim = style().Foreground(lipgloss.Color("240"))
	t.Selected = style().Bold(true).Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("60"))
	t.Agent = style().Foreground(lipgloss.Color("141"))
	t.Human = style().Foreground(lipgloss.Color("114"))
	t.Err = style().Bold(true).Foreground(lipgloss.Color("203"))
	t.OK = style().Foreground(lipgloss.Color("114"))
	t.Badge = style().Foreground(lipgloss.Color("179"))
	t.Bar = style().Foreground(lipgloss.Color("39"))
	// The queue's three states, in the console's three meanings and as close to
	// its three colours as a 256-colour cube gets: amber #e0a03f, grey #8b93a7,
	// green #4fae7a. Amber for what is in flight, because that is where a reader
	// opening the panel should land first; green for done, which is the one
	// convention here strong enough to be worth obeying; grey for waiting,
	// deliberately quiet, because a queue is mostly waiting and if waiting shouts
	// then nothing does.
	//
	// Three distinct colours and not two: done and waiting sharing Dim was the
	// gap this closes, and a finished item that looks exactly like an untouched
	// one is a queue with two states in it rather than three.
	t.TodoActive = style().Foreground(lipgloss.Color("179"))
	t.TodoOpen = style().Foreground(lipgloss.Color("245"))
	t.TodoDone = style().Foreground(lipgloss.Color("114"))
	t.Severity = map[string]lipgloss.Style{
		"info":        style().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")),
		"warning":     style().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214")),
		"maintenance": style().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("179")),
		"breaking":    style().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("160")),
	}
	return t
}

// EnvTheme is the theme for this process's environment.
func EnvTheme() Theme {
	return NewTheme(DetectProfile(os.Getenv, lipgloss.ColorProfile()), unicodeOK(os.Getenv))
}

// severityStyle is the banner colour for a severity, falling back to info for
// anything the node grows later.
func (t Theme) severityStyle(severity string) lipgloss.Style {
	if s, ok := t.Severity[severity]; ok {
		return s
	}
	return t.Severity["info"]
}

// ------------------------------------------------------------------ drawing

// truncate cuts a string to width, by runes rather than by bytes, and marks
// that it cut. It is used on everything that goes into a fixed column, so a
// 60-character title in a 30-column pane wraps nothing and breaks no layout.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

// truncateASCII is truncate for a terminal with no unicode.
func (t Theme) clip(s string, width int) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if !t.Unicode {
		if width <= 0 {
			return ""
		}
		runes := []rune(s)
		if len(runes) <= width {
			return s
		}
		if width <= 3 {
			return string(runes[:width])
		}
		return string(runes[:width-3]) + "..."
	}
	return truncate(s, width)
}

// pad right-fills to width so a background colour covers the whole line and the
// column beside it starts where it should.
//
// The width it counts is the printable one: a styled string is mostly escape
// bytes, and padding by len() would leave every coloured cell short by however
// many bytes its colour took to say.
func pad(s string, width int) string {
	n := lipgloss.Width(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// bar draws a horizontal meter of width cells for value out of max.
func (t Theme) bar(value, max float64, width int) string {
	if width <= 0 {
		return ""
	}
	full, empty := "#", "."
	if t.Unicode {
		full, empty = "█", "░"
	}
	if max <= 0 || value <= 0 {
		return strings.Repeat(empty, width)
	}
	filled := int((value/max)*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	if filled < 1 {
		filled = 1
	}
	return strings.Repeat(full, filled) + strings.Repeat(empty, width-filled)
}

// sparkline draws a series in one line of cells.
func (t Theme) sparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	levels := []rune("_.-=+*#%")
	if t.Unicode {
		levels = []rune("▁▂▃▄▅▆▇█")
	}
	var b strings.Builder
	for _, v := range values {
		if max <= 0 {
			b.WriteRune(levels[0])
			continue
		}
		idx := int(v / max * float64(len(levels)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(levels) {
			idx = len(levels) - 1
		}
		b.WriteRune(levels[idx])
	}
	return b.String()
}

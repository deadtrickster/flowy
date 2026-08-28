module github.com/deadtrickster/flowy

// 1.24 IS A REQUIREMENT, NOT A PREFERENCE. internal/store/inbox.go tags the
// waiter's process claim `omitzero`, which arrived in 1.24. An older toolchain
// does not reject the tag - it IGNORES it, and the field is emitted always. The
// three fields inside carry omitempty, so a waiter that has claimed no process
// comes out of /api/presence as `{}` rather than not at all: empty where the
// answer is absent, which is the one defect this repo has a rule about.
//
// Measured 2026-08-28: the suite is green on go1.26 here and red on go1.22.2 in
// a guest, on the same commit, at
// `a listener that claimed no process is null, want <not said>`.
//
// Stated here rather than fixed at the tag because the go directive is a HARD
// MINIMUM since 1.21: with this line an old toolchain refuses to build, which
// is a red that names itself, instead of building something whose answers are
// quietly a different shape.
go 1.24

require github.com/lib/pq v1.10.9

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymanbagabas/go-udiff v0.2.0 // indirect
	github.com/charmbracelet/x/ansi v0.4.5 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20240815200342-61de596daa2b // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

require (
	github.com/charmbracelet/bubbles v0.20.0
	github.com/charmbracelet/bubbletea v1.2.4
	github.com/charmbracelet/lipgloss v1.0.0
	github.com/charmbracelet/x/exp/teatest v0.0.0-20240815200342-61de596daa2b
	github.com/coder/websocket v1.8.13
	github.com/hanwen/go-fuse/v2 v2.9.0
	github.com/muesli/termenv v0.15.2
	golang.org/x/crypto v0.31.0
	golang.org/x/sys v0.28.0
)

// Package web holds the built console, compiled into the node binary. `flowy
// serve` is one file: there is no directory of assets to deploy next to it and
// no way for the console and the node to be different versions of themselves.
//
// The embed lives here rather than in internal/flowy because go:embed cannot
// climb with "..": a package at any depth below the repo root cannot name
// web/dist. Package web sits beside it and embeds the tree it is inside.
package web

import "embed"

// Dist is the built single-page app, as Vite left it under web/dist.
//
// The `all:` prefix matters twice: it keeps Vite's dotfiles, and it lets this
// embed succeed on a tree where the console has not been built yet - web/dist
// holds a tracked .gitkeep, so the pattern always matches something and `go
// build` works before `npm run build` has ever run. When that is all it
// matches, the handler says so rather than pretending to be an app.
//
//go:embed all:dist
var Dist embed.FS

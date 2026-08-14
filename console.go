package main

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// consoleFS is the built console, compiled into the binary. `flowy serve` is
// one file: there is no directory of assets to deploy next to it and no way for
// the console and the node to be different versions of themselves.
//
// The `all:` prefix matters twice: it keeps Vite's dotfiles, and it lets this
// embed succeed on a tree where the console has not been built yet - web/dist
// holds a tracked .gitkeep, so the pattern always matches something and `go
// build` works before `npm run build` has ever run. When that is all it
// matches, the handler says so rather than pretending to be an app.
//
//go:embed all:web/dist
var consoleFS embed.FS

// console serves the built single-page app.
type console struct {
	files fs.FS
	index []byte
	// started is the modtime handed to http.ServeContent for the index. The
	// embedded files all carry the zero time, and a zero modtime turns caching
	// validators off, which is what we want for index.html anyway.
	started time.Time
}

// newConsole opens the embedded build. It reports whether there is an app in
// there: a tree that has never run `npm run build` embeds an empty directory,
// and the node still has to start - the API is the half of it that does not
// need a bundler.
func newConsole() (*console, bool) {
	sub, err := fs.Sub(consoleFS, "web/dist")
	if err != nil {
		return &console{}, false
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return &console{files: sub}, false
	}
	return &console{files: sub, index: index, started: time.Now()}, true
}

// ServeHTTP serves an asset when the path names one and index.html when it does
// not. That fallback is the whole point of routing by path: /chat/general is a
// route inside the app, the browser asks this server for it on a reload or a
// pasted link, and the app can only take it from there if the server answers
// with the app rather than with 404.
//
// It is only ever reached for paths outside /api/ - unknown API paths answer
// 404 as themselves, because a client that asked for JSON and got HTML back
// with a 200 has to parse the app to find that out.
func (c *console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("method not allowed"))
		return
	}
	if c.index == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":   "console not built",
			"hint":    "cd web && npm ci && npm run build, then rebuild flowy",
			"version": version,
		})
		return
	}

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" && name != "." {
		if f, err := c.files.Open(name); err == nil {
			defer f.Close()
			if st, err := f.Stat(); err == nil && !st.IsDir() {
				if rs, ok := f.(io.ReadSeeker); ok {
					// Hashed asset names, so the content of one never changes.
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					http.ServeContent(w, r, st.Name(), st.ModTime(), rs)
					return
				}
			}
		}
	}

	// Every other path is a route in the app.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", c.started, strings.NewReader(string(c.index)))
}

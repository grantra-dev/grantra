// Package webui embeds the built React console. `make web` copies the Vite
// build from web/dist into dist/ before the Go build; without it only the
// placeholder home page is served.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// Files returns the built console, or false when it was not built into this
// binary.
func Files() (fs.FS, bool) {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

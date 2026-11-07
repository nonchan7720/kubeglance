// Package ui embeds the browser UI: plain HTML, CSS and JavaScript, no build
// step, nothing loaded from a CDN. The UI is mobile first: one screen per
// level, one thumb.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler serves the UI at /.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return http.FileServerFS(sub)
}

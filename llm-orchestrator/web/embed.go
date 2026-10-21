// Package web embeds the dashboard assets into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static is the web root (index.html, app.css, app.js).
var Static, _ = fs.Sub(files, "static")

// Package wrongtrace is the module root. Its only job is to own the
// //go:embed directive for the compiled React dashboard so internal packages
// can consume the assets. go:embed cannot reference parent directories, so the
// directive must live at the repository root next to web/dist.
package wrongtrace

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var webDistFS embed.FS

// WebDist returns the web/dist sub-filesystem containing the compiled React
// dashboard. The "all:" prefix includes dot-prefixed files if the frontend
// ever ships them.
func WebDist() (fs.FS, error) {
	return fs.Sub(webDistFS, "web/dist")
}

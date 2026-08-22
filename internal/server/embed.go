package server

import (
	"io/fs"

	wrongtrace "github.com/wrongstack/wrongtrace"
)

// WebDistFS returns the embedded React dashboard's web/dist sub-filesystem.
// The //go:embed directive lives in the module-root package (embed.go) because
// embed patterns cannot escape their own package directory.
func WebDistFS() (fs.FS, error) {
	return wrongtrace.WebDist()
}

//go:build !windows && !linux && !darwin

package core

import (
	"os"
	"time"
)

// fileBirthTime is unavailable on this platform; first sightings baseline.
func fileBirthTime(string, os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}

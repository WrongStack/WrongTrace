//go:build !windows

package ipc

import (
	"net"
	"time"
)

// dialSocket dials a Unix Domain Socket with a deadline.
func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

//go:build windows

package ipc

import (
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
)

// dialSocket dials a Windows Named Pipe with a deadline.
func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(path, &timeout)
}

//go:build !windows

package ipc

import "net"

// bindWindowsPipe exists only so bindSocket compiles on POSIX; the
// runtime.GOOS branch in socket.go never routes here.
func bindWindowsPipe(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}

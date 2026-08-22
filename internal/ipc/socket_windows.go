//go:build windows

package ipc

import (
	"net"

	winio "github.com/Microsoft/go-winio"
)

// bindWindowsPipe listens on a Windows Named Pipe (\\.\pipe\<name>). Stream
// mode matches Unix socket semantics.
func bindWindowsPipe(path string) (net.Listener, error) {
	return winio.ListenPipe(path, &winio.PipeConfig{
		MessageMode:      false,
		InputBufferSize:  64 * 1024,
		OutputBufferSize: 64 * 1024,
	})
}

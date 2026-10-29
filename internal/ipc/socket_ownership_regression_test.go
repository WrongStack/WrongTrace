//go:build !windows

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func dialable(path string) bool {
	c, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// A socket a live server still answers on is never reclaimed: a second Start
// on the same path fails and the first server stays reachable.
func TestStart_RefusesLiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.sock")
	a := &Server{cfg: Config{Engine: &fakeSink{}, SocketPath: path}}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b := &Server{cfg: Config{Engine: &fakeSink{}, SocketPath: path}}
	if err := b.Start(); err == nil {
		b.Stop()
		t.Fatal("second Start replaced a live server's socket")
	}
	if !dialable(path) {
		t.Fatal("first server unreachable after a refused second Start")
	}
}

// Stop removes only the socket file it created. A successor that bound a new
// socket at the same path (after the old file was removed) keeps it.
func TestStop_KeepsSuccessorSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	a := &Server{cfg: Config{Engine: &fakeSink{}, SocketPath: path}}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil { // e.g. a cleanup script
		t.Fatal(err)
	}
	b := &Server{cfg: Config{Engine: &fakeSink{}, SocketPath: path}}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	a.Stop()
	if !dialable(path) {
		t.Fatal("stale Stop deleted the successor's live socket")
	}
}

// Controls: a stale socket (nobody listening) is reclaimed, and Stop removes
// a socket that is still its own.
func TestStart_ReclaimsStaleSocketAndStopCleansOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	s := &Server{cfg: Config{Engine: &fakeSink{}, SocketPath: path}}
	if err := s.Start(); err != nil {
		t.Fatalf("stale socket not reclaimed: %v", err)
	}
	s.Stop()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Stop left its own socket behind: %v", err)
	}
}

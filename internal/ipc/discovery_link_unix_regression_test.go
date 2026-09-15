//go:build unix

package ipc

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempDiscoveryLink redirects the well-known /tmp symlink into a temp dir.
func useTempDiscoveryLink(t *testing.T) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "wrongtrace.sock")
	old := discoveryLinkPath
	discoveryLinkPath = link
	t.Cleanup(func() { discoveryLinkPath = old })
	return link
}

// The discovery link used to be os.Remove'd and re-created with both errors
// ignored, so any entry squatting the name was clobbered (or, in a sticky
// /tmp, silently kept pointing agents at someone else's socket). Only a
// symlink owned by us may be replaced now; anything else is left alone.
func TestPublishDiscoveryLink_LeavesForeignEntryUntouched(t *testing.T) {
	link := useTempDiscoveryLink(t)
	if err := os.WriteFile(link, []byte("squatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if publishDiscoveryLink("/nonexistent/target.sock", link) {
		t.Fatal("publishDiscoveryLink replaced a regular file squatting the link name")
	}
	if b, err := os.ReadFile(link); err != nil || string(b) != "squatter" {
		t.Fatalf("squatting entry was modified: %q, %v", b, err)
	}
}

func TestPublishDiscoveryLink_ReplacesOwnStaleSymlink(t *testing.T) {
	link := useTempDiscoveryLink(t)
	if err := os.Symlink("/old/target.sock", link); err != nil {
		t.Fatal(err)
	}
	if !publishDiscoveryLink("/new/target.sock", link) {
		t.Fatal("own stale symlink was not replaced")
	}
	if dest, err := os.Readlink(link); err != nil || dest != "/new/target.sock" {
		t.Fatalf("link -> %q, %v; want /new/target.sock", dest, err)
	}
}

// Stop removes the discovery symlink it published (only while it still points
// at our socket) along with the socket file.
func TestServerStop_RemovesDiscoveryLinkAndSocket(t *testing.T) {
	link := useTempDiscoveryLink(t)
	path := testSocketPath(t)
	srv := NewServer(Config{SocketPath: path, Engine: &fakeSink{}})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if dest, err := os.Readlink(link); err != nil || dest != path {
		srv.Stop()
		t.Fatalf("discovery link -> %q, %v; want %q", dest, err, path)
	}
	srv.Stop()
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("discovery link survived Stop: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket file survived Stop: %v", err)
	}
}

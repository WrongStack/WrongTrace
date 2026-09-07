package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestEscapeSQLiteURIPath pins the escaping contract: '%' must be encoded
// first (so the escapes this function emits are not themselves re-encoded),
// then '?' and '#'. Every other byte — backslashes, spaces, parens,
// non-ASCII — passes through untouched: the driver pre-splits only on a raw
// '?', and SQLite's URI layer treats all other bytes as ordinary path data.
func TestEscapeSQLiteURIPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\data\wrongtrace.db`, `C:\data\wrongtrace.db`},
		{"pct%41est", "pct%2541est"},
		{"100%zzpro", "100%25zzpro"}, // invalid hex still escaped: literal round-trip guaranteed
		{"/tmp/a?b.db", "/tmp/a%3Fb.db"},
		{"/tmp/a#b.db", "/tmp/a%23b.db"},
		{"mix%41?x#y", "mix%2541%3Fx%23y"},
		{"wrong trace dir", "wrong trace dir"},
	}
	for _, c := range cases {
		if got := escapeSQLiteURIPath(c.in); got != c.want {
			t.Errorf("escapeSQLiteURIPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOpen_PathWithURIProtectedCharacters pins the round-36 root cause: a
// database path containing a valid %XX sequence used to be percent-DECODED by
// SQLite's URI layer (the DSN embedded the raw path), so Open failed with
// SQLITE_CANTOPEN 14 — the decoded directory never exists. The contract is
// byte-exact: the database lives at exactly the requested path, no
// decoded-sibling artifact appears, and data round-trips through a reopen.
func TestOpen_PathWithURIProtectedCharacters(t *testing.T) {
	root := t.TempDir()

	t.Run("valid escape in directory name", func(t *testing.T) {
		target := filepath.Join(root, "pct%41est", "wrongtrace.db")
		seedAndRoundTrip(t, target)
		if _, err := os.Stat(filepath.Join(root, "pctAtest")); err == nil {
			t.Fatalf("stray decoded-path artifact exists: %s", filepath.Join(root, "pctAtest"))
		}
	})

	t.Run("invalid escape is literal", func(t *testing.T) {
		seedAndRoundTrip(t, filepath.Join(root, "100%zzpro", "wrongtrace.db"))
	})

	t.Run("plain path unaffected", func(t *testing.T) {
		seedAndRoundTrip(t, filepath.Join(root, "plain", "wrongtrace.db"))
	})
}

// TestOpen_URIReservedNameCharacters covers '?' and '#', which are legal
// filename characters on POSIX only; Windows cannot even create such paths,
// so those subtests run there only. The escaping itself is pinned on every
// platform by TestEscapeSQLiteURIPath.
func TestOpen_URIReservedNameCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?'/#' cannot appear in NTFS filenames; exercised on POSIX")
	}
	root := t.TempDir()
	seedAndRoundTrip(t, filepath.Join(root, "a?b", "wrongtrace.db"))
	seedAndRoundTrip(t, filepath.Join(root, "a#b", "wrongtrace.db"))
}

// seedAndRoundTrip opens the store at target, migrates, writes one run row,
// closes, and reopens to prove the database physically lives at the exact
// requested path and the data survived.
func seedAndRoundTrip(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := Open(target)
	if err != nil {
		t.Fatalf("Open(%q): %v", target, err)
	}
	if err := st.Migrate(); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	err = st.UpsertRun(RunRecord{
		RunID:     "dsn-path-proof",
		TaskID:    "round-36",
		AgentName: "test",
		ModelName: "test-model",
		Provider:  "test",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		st.Close()
		t.Fatalf("UpsertRun: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("database missing at requested path %q: %v", target, err)
	}
	st2, err := Open(target)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	ov, err := st2.Overview()
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.TotalRuns < 1 {
		t.Fatalf("round-trip lost the run row: TotalRuns=%d", ov.TotalRuns)
	}
}

package db

import (
	"path/filepath"
	"testing"
	"time"
)

// Go pointer-receiver methods are stored as "method:<file>::*T.M". Asking for
// "*T.M" or the method-expression spelling "(*T).M" must return exactly that
// method's history, never an unfiltered timeline of every symbol.
func TestSymbolHistory_PointerReceiverSpellings(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	const path = "internal/srv/server.go"
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, s := range []struct{ id, sig string }{
		{"h1", "method:server.go::*Server.Handle"},
		{"h2", "method:server.go::*Server.Handle"},
		{"n1", "method:server.go::Server.Name"},
		{"x1", "function:server.go::helper"},
	} {
		if err := st.InsertEvent(EventRecord{EventID: s.id, RepoName: "r", FilePath: path, Signature: s.sig,
			NodeType: "method", Action: "MODIFIED", OccurredAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		file, sig string
		want      int
	}{
		{path, "method:server.go::*Server.Handle", 2},
		{path, "*Server.Handle", 2},
		{path, "(*Server).Handle", 2},
		{"", "(*Server).Handle", 2},
		{path, "Server.Name", 1},
		{path, "(Server).Name", 1},
		{path, "helper(string)", 1},
		{path, "(", 0},
	} {
		rows, err := st.SymbolHistory(c.file, c.sig, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != c.want {
			t.Errorf("SymbolHistory(%q, %q) = %d rows; want %d", c.file, c.sig, len(rows), c.want)
		}
	}
}

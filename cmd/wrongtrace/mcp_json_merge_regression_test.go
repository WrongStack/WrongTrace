package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// `init` in a project that already has a .mcp.json must register wrongtrace
// beside the existing servers, keep every other key, and refuse to rewrite a
// file it cannot parse.
func TestMergeMCPServer(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"mcpServers":{"github":{"command":"gh-mcp"}},"other":1}`)
	added, err := mergeMCPServer(p)
	if err != nil || !added {
		t.Fatalf("merge = %v, %v; want added", added, err)
	}
	raw, _ := os.ReadFile(p)
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
		Other int `json:"other"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["github"].Command != "gh-mcp" || doc.Other != 1 ||
		doc.MCPServers["wrongtrace"].Command != "wrongtrace" || len(doc.MCPServers["wrongtrace"].Args) != 1 {
		t.Fatalf("merged file = %s", raw)
	}

	// Second run is a no-op.
	if added, err := mergeMCPServer(p); err != nil || added {
		t.Fatalf("re-merge = %v, %v; want no change", added, err)
	}

	// No mcpServers key yet.
	write(`{"x":true}`)
	if added, err := mergeMCPServer(p); err != nil || !added {
		t.Fatalf("merge without mcpServers = %v, %v", added, err)
	}

	// Unparseable or wrongly shaped files are left byte-for-byte alone.
	for _, bad := range []string{`{not json`, `[]`, `{"mcpServers":[1]}`} {
		write(bad)
		if _, err := mergeMCPServer(p); err == nil {
			t.Fatalf("merge of %q succeeded; want error", bad)
		}
		if got, _ := os.ReadFile(p); string(got) != bad {
			t.Fatalf("file rewritten to %q; want %q", got, bad)
		}
	}
}

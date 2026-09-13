package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanAgentSessions_ClaudeCountsOnlyOwnEncodedProject pins the Claude Code
// discovery contract: ~/.claude/projects holds ONE directory per project cwd,
// named by replacing every non-alphanumeric character of the cwd with '-'
// ("D:\Codebox\PROJECTS\WrongTrace" -> "d--Codebox-PROJECTS-WrongTrace").
// Matching by the bare base name made any sibling path that merely contains
// it (WrongTrace-docs, WrongTrace-v2) leak its transcript count into this
// project's discovered_sessions.claude_code.
//
// The fixture dir names are COMPUTED from this test's actual root with the
// production encoding rule, so they stay self-consistent wherever the temp
// directory lands — a hardcoded real-machine spelling would encode a path
// that does not exist here and would not match the post-fix equality test.
func TestScanAgentSessions_ClaudeCountsOnlyOwnEncodedProject(t *testing.T) {
	fixture := t.TempDir()
	home := filepath.Join(fixture, "home")
	root := filepath.Join(fixture, "ws", "WrongTrace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	// Isolate the production path's environment before the scan reads it:
	// os.UserHomeDir resolves USERPROFILE on Windows and HOME on POSIX, and
	// the Cursor/Windsurf/Trae/Cline arms read APPDATA.
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(fixture, "appdata"))

	own := encodeClaudeProjectDir(root)                     // this project's encoded cwd
	sibling := encodeClaudeProjectDir(root + "-docs")       // sibling checkout: base name still matches
	unrelated := encodeClaudeProjectDir(filepath.Join(fixture, "ws", "UnrelatedProject"))

	proj := func(dirName string, transcripts int) {
		t.Helper()
		dir := filepath.Join(home, ".claude", "projects", dirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for i := 0; i < transcripts; i++ {
			f := filepath.Join(dir, fmt.Sprintf("transcript-%02d.jsonl", i))
			if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
		}
	}

	// Case variant on the own directory pins the case-insensitive match:
	// real machines hold both "D--..." and "d--..." spellings.
	proj(upperFirst(own), 2)      // own project — counts
	proj(sibling, 1)              // sibling project — must not count
	proj(strings.ToLower(unrelated), 3) // unrelated project — must not count

	counts := ScanAgentSessions(root)
	if got := counts["claude_code"]; got != 2 {
		t.Fatalf("claude_code = %d, want 2 (own transcripts only; sibling WrongTrace-docs and unrelated UnrelatedProject are different projects)", got)
	}
}

// upperFirst capitalizes the first rune, mirroring the drive-letter casing
// variance observed in real ~/.claude/projects layouts.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

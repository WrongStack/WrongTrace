package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit_ReportsWriteFailuresHonestly pins the round-49 contract: when a
// config-file write fails, runInit must report the failure per file (✗) and
// must NOT print "✓ Created" for a file that does not exist. The pre-fix code
// discarded every os.WriteFile error (`_ =`) and printed an unconditional
// "✓ Created" — a write failure was reported as success with exit 0.
//
// Injection: icacls denies file creation (WD) on the working directory;
// Getwd/Stat still work, so every os.WriteFile fails with access denied.
func TestInit_ReportsWriteFailuresHonestly(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	user := os.Getenv("USERNAME")
	if user == "" {
		t.Skip("USERNAME env is empty; cannot scope the icacls deny")
	}
	denyOut, denyErr := exec.Command("icacls", work, "/deny", user+":(WD)").CombinedOutput()
	if denyErr != nil {
		t.Fatalf("icacls deny: %v\n%s", denyErr, denyOut)
	}
	// Lift the denial before t.TempDir cleanup, or cleanup cannot delete the tree.
	t.Cleanup(func() {
		_, _ = exec.Command("icacls", work, "/remove:d", user).CombinedOutput()
	})

	t.Chdir(work)

	// Capture stdout: fmt.Printf writes through the os.Stdout variable, so a
	// pipe swap captures runInit's per-file reporting.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	initErr := runInit(initCmd, nil)
	w.Close()
	os.Stdout = old

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, rerr := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	out := string(buf)

	if initErr != nil {
		t.Fatalf("runInit must complete best-effort with per-file failure reporting, got error: %v", initErr)
	}

	missing := 0
	for _, name := range []string{".mcp.json", "CLAUDE.md", "AGENTS.md", ".cursorrules"} {
		if _, statErr := os.Stat(filepath.Join(work, name)); os.IsNotExist(statErr) {
			missing++
		}
	}
	// Deny-bite guard: if nothing is missing, the denial did not take effect
	// (elevated shell) and this host cannot exercise the failure path.
	if missing == 0 {
		t.Skip("icacls denial did not take effect (all 4 config files exist); cannot exercise the failure path here")
	}

	if !strings.Contains(out, "✗ Could not create") {
		t.Fatalf("failed writes were not reported: output missing ✗ Could-not-create lines:\n%s", out)
	}
	if strings.Contains(out, "✓ Created") {
		t.Fatalf("init claimed ✓ Created for config files that do not exist on disk:\n%s", out)
	}
	if missing != 4 {
		t.Fatalf("expected all 4 config files to be missing under the denial, got %d/4 missing", 4-missing)
	}
}

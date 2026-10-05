package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// lockDirForWrite removes write permission from a directory so every config
// write inside fails. On Windows the mode bits are not enforced for directories,
// so an explicit deny ACL is applied instead.
func lockDirForWrite(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("icacls", dir, "/deny", "*S-1-1-0:(OI)(CI)W", "/T", "/C").CombinedOutput(); err != nil {
			t.Skipf("icacls deny failed (%v): %s", err, out)
		}
		t.Cleanup(func() {
			_ = exec.Command("icacls", dir, "/remove:d", "*S-1-1-0", "/T", "/C").Run()
			_ = os.Chmod(dir, 0o755)
		})
		return
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestInitSummaryNeverClaimsCompleteWhenWritesFail pins the contract that a
// command's closing verdict must agree with its own per-step output.
//
// runInit writes .mcp.json, CLAUDE.md, AGENTS.md and .cursorrules. Each write
// reported failure honestly ("✗ Could not create …"), but the closing line was
// an unconditional "✨ Setup complete!", so a read-only project directory — the
// exact case a user hits in a locked-down or container-mounted repo — printed
// four failure lines and then declared success at exit 0. This is the same
// self-contradiction fixed for runDoctor (round 77) and runTrace (round 80).
func TestInitSummaryNeverClaimsCompleteWhenWritesFail(t *testing.T) {
	dir := t.TempDir()

	// runInit targets os.Getwd(), so enter the directory BEFORE locking it: the
	// deny ACL also blocks chdir, so locking first would abort the test at
	// setup. This ordering (CWD set, then writes denied) mirrors exactly what
	// the standalone proof does — the process starts in the dir, then every
	// write inside it fails.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	lockDirForWrite(t, dir)

	rootCmd.SetArgs([]string{"init"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	out := captureStdout(t, func() { _ = rootCmd.Execute() })

	if !strings.Contains(out, "Could not create") {
		t.Skipf("setup drift: writes did not fail in the locked dir, so a "+
			"'Setup complete!' verdict would be honest:\n%s", out)
	}
	if strings.Contains(out, "✨ Setup complete!") {
		t.Errorf("init reported %d failed writes yet still printed %q — the "+
			"closing verdict contradicts its own output:\n%s",
			strings.Count(out, "Could not create"), "✨ Setup complete!", out)
	}
	if !strings.Contains(out, "Setup incomplete") {
		t.Errorf("failed writes must produce an explicit negative verdict:\n%s", out)
	}
}

// TestInitSummaryClaimsCompleteWhenWritesSucceed is the control: on a writable
// directory every config file is created and the success verdict is legitimate.
// It fails if the fix suppresses the success line instead of conditioning it.
func TestInitSummaryClaimsCompleteWhenWritesSucceed(t *testing.T) {
	dir := t.TempDir()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	rootCmd.SetArgs([]string{"init"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	out := captureStdout(t, func() { _ = rootCmd.Execute() })

	if strings.Contains(out, "Could not create") {
		t.Fatalf("CONTROL BROKEN: a writable dir reported a write failure:\n%s", out)
	}
	if !strings.Contains(out, "✨ Setup complete!") {
		t.Errorf("a fully successful init must keep the success verdict:\n%s", out)
	}
	// And the files must actually be there — the verdict must track reality.
	for _, name := range []string{".mcp.json", "CLAUDE.md", "AGENTS.md", ".cursorrules"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("init claimed success but %s was not created: %v", name, err)
		}
	}
}

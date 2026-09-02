package main

// Regression coverage for git hook ownership (bug-hunt round 13). The
// pre-fix runHook overwrote any existing post-commit hook on install —
// destroying husky or custom user hooks, including via `wrongtrace init` —
// and removed whatever hook was present on uninstall. Hook ownership is now
// marked by the "WrongTrace" text in the script: install and uninstall
// refuse to touch a foreign hook and stay idempotent on our own.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const foreignHookScript = "#!/bin/sh\necho my-custom-hook >> hook-ran.txt\n"

func newHookRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func postCommitHook(dir string) string {
	return filepath.Join(dir, ".git", "hooks", "post-commit")
}

// Installing over a foreign hook must refuse and leave the hook untouched.
func TestHookInstallRefusesForeignHook(t *testing.T) {
	dir := newHookRepo(t)
	hookFile := postCommitHook(dir)
	if err := os.WriteFile(hookFile, []byte(foreignHookScript), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runHook(nil, []string{"install"}); err == nil {
		t.Fatal("install succeeded over a foreign hook; expected refusal")
	}
	data, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "my-custom-hook") {
		t.Fatalf("existing post-commit hook was overwritten: %q", string(data))
	}
}

// Installing into a repo without a post-commit hook succeeds, and
// re-installing over our own hook stays idempotent.
func TestHookInstallIsIdempotentOnOwnHook(t *testing.T) {
	dir := newHookRepo(t)
	for i := 0; i < 2; i++ {
		if err := runHook(nil, []string{"install"}); err != nil {
			t.Fatalf("install #%d errored: %v", i+1, err)
		}
	}
	data, err := os.ReadFile(postCommitHook(dir))
	if err != nil || !strings.Contains(string(data), "WrongTrace") {
		t.Fatalf("our hook missing after re-install: %q", string(data))
	}
}

// Uninstalling must refuse to touch a foreign hook, and must remove our own.
func TestHookUninstallPreservesForeignHook(t *testing.T) {
	dir := newHookRepo(t)
	hookFile := postCommitHook(dir)
	if err := os.WriteFile(hookFile, []byte(foreignHookScript), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runHook(nil, []string{"uninstall"}); err == nil {
		t.Fatal("uninstall removed a foreign hook without refusing")
	}
	data, _ := os.ReadFile(hookFile)
	if !strings.Contains(string(data), "my-custom-hook") {
		t.Fatal("uninstall deleted a foreign hook")
	}

	ours := "#!/bin/sh\n# WrongTrace automatic post-commit telemetry ping\nif command -v wrongtrace >/dev/null 2>&1; then\n  wrongtrace status >/dev/null 2>&1 &\nfi\n"
	if err := os.WriteFile(hookFile, []byte(ours), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runHook(nil, []string{"uninstall"}); err != nil {
		t.Fatalf("uninstall of our own hook errored: %v", err)
	}
	if _, err := os.Stat(hookFile); !os.IsNotExist(err) {
		t.Fatal("our own hook still present after uninstall")
	}
}

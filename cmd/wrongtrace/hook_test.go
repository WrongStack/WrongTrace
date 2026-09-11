package main

// Regression coverage for git hook ownership (bug-hunt round 13) and
// worktree support + exact ownership markers (bug-hunt round 7). The pre-fix
// runHook overwrote any existing post-commit hook on install — destroying
// husky or custom user hooks, including via `wrongtrace init` — and removed
// whatever hook was present on uninstall. Ownership is now marked by the
// exact telemetry comment line the installer writes: a foreign hook that
// merely mentions "WrongTrace" is refused on both install and uninstall.
// Linked Git worktrees (`.git` is a `gitdir:` pointer file, not a directory)
// resolve to the repo's common dir, where shared hooks live.

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

// A foreign hook that merely mentions WrongTrace must still be treated as
// foreign: only the exact telemetry comment line the installer writes counts
// as ownership.
func TestHookRefusesForeignHookMentioningWrongTrace(t *testing.T) {
	dir := newHookRepo(t)
	hookFile := postCommitHook(dir)
	foreign := "#!/bin/sh\n# Forward commit stats to the WrongTrace dashboard\necho my-custom-hook >> hook-ran.txt\n"
	if err := os.WriteFile(hookFile, []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runHook(nil, []string{"install"}); err == nil {
		t.Fatal("install overwrote a foreign hook that mentions WrongTrace")
	}
	if err := runHook(nil, []string{"uninstall"}); err == nil {
		t.Fatal("uninstall removed a foreign hook that mentions WrongTrace")
	}
	data, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "my-custom-hook") {
		t.Fatalf("foreign hook was modified: %q", string(data))
	}
}

// A linked Git worktree stores .git as a file ("gitdir: <path>"), not a
// directory. Hook install/uninstall must follow the pointer to the repo's
// common dir instead of failing with "not a git repository", and must touch
// the shared hooks directory, not the worktree-local one.
func TestHookInstallSupportsGitFileWorktree(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitdirTarget := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitdirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt") // separate tree, like a real worktree
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + gitdirTarget + "\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(wt)

	if err := runHook(nil, []string{"install"}); err != nil {
		t.Fatalf("install inside a git worktree errored: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(main, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatalf("hook not installed in the common dir: %v", err)
	}
	if !strings.Contains(string(data), wrongTraceHookMarker) {
		t.Fatalf("installed hook missing ownership marker: %q", string(data))
	}

	if err := runHook(nil, []string{"uninstall"}); err != nil {
		t.Fatalf("uninstall inside a git worktree errored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatal("worktree uninstall left the hook behind")
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

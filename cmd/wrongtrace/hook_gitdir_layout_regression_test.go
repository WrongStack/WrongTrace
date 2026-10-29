package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Without a git binary the hooks dir comes from the .git pointer file. A
// linked worktree's gitdir names the shared repository in `commondir`; a
// submodule's gitdir has none and owns its hooks. Inferring the common dir
// from the layout (two levels up) sent a submodule's hook into the
// superproject.
func TestCommonDirFromGitdirFile_SubmoduleAndWorktree(t *testing.T) {
	root := t.TempDir()
	mk := func(p ...string) string {
		d := filepath.Join(append([]string{root}, p...)...)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	superGit := mk("super", ".git")

	// Submodule: <super>/sub/.git -> ../.git/modules/sub (no commondir).
	subModGit := mk("super", ".git", "modules", "sub")
	sub := mk("super", "sub")
	write(filepath.Join(sub, ".git"), "gitdir: ../.git/modules/sub\n")
	if got, ok := commonDirFromGitdirFile(filepath.Join(sub, ".git"), sub); !ok || got != subModGit {
		t.Errorf("submodule resolved to %q (ok=%v); want its own gitdir %q", got, ok, subModGit)
	}

	// Linked worktree: gitdir <super>/.git/worktrees/wt with commondir "../..".
	wtGit := mk("super", ".git", "worktrees", "wt")
	write(filepath.Join(wtGit, "commondir"), "../..\n")
	wt := mk("wt")
	write(filepath.Join(wt, ".git"), "gitdir: "+wtGit+"\n")
	if got, ok := commonDirFromGitdirFile(filepath.Join(wt, ".git"), wt); !ok || got != superGit {
		t.Errorf("worktree resolved to %q (ok=%v); want the common dir %q", got, ok, superGit)
	}
}

package wrongtrace

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestCommittedDistIndexIsPlaceholder mechanizes the placeholder contract:
// the COMMITTED web/dist/index.html must be the build placeholder, never the
// built dashboard. The built index references /assets/* bundles that are
// gitignored, so a fresh clone embedding it would serve a broken dashboard
// shell and depend on files nobody has.
//
// This contract was previously enforced only by review discipline (and was
// flagged stale repeatedly); this test turns it into a CI invariant.
//
// It deliberately reads the blob from git ("git show HEAD:...") rather than
// the working tree: local development legitimately has the BUILT index on
// disk after `npm run build` (it is embedded into the binary at compile
// time), and that mixed state must not fail the test. CI's Go job checks
// out a clean tree, so committed == disk there.
func TestCommittedDistIndexIsPlaceholder(t *testing.T) {
	// The root package lives at the repository root, so the test's working
	// directory is the repo root.
	cmd := exec.Command("git", "show", "HEAD:web/dist/index.html")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := err.Error() + ": " + stderr.String()
		switch {
		case strings.Contains(msg, "does not exist"), strings.Contains(msg, "does not have"):
			t.Fatalf("web/dist/index.html is not tracked at HEAD — the placeholder MUST be committed so fresh clones satisfy //go:embed all:web/dist with a working placeholder UI (see README build notes): %s", msg)
		default:
			// No git, or not a repository (e.g., a source tarball): the
			// "committed" notion does not exist here. Loud skip — never a
			// silent pass — with the reason.
			t.Skipf("cannot verify the committed placeholder contract (git unavailable or not a repo): %s", msg)
		}
		return
	}

	idx := stdout.String()
	if strings.Contains(idx, "/assets/") {
		t.Fatalf(`committed web/dist/index.html references /assets/ paths — the BUILT dashboard index was committed instead of the placeholder.

Fresh clones would embed an index pointing at gitignored bundles, serving a broken dashboard shell until ` + "`make build-ui`" + ` runs.

Fix: restore the placeholder, then amend the commit:
    git restore --source HEAD -- web/dist/index.html
The placeholder is the version documented in the README build notes.`)
	}
}

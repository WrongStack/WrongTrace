package ipc

import (
	"os"
	"strings"
	"testing"
)

// TestChangeLogRepoNameGuardDocumentedAsRemoved pins the documentation half of
// the report_file_read contract.
//
// The dispatch deliberately accepts a missing repo_name and forwards
// RepoName="" so Engine.RecordReadEvent can resolve the owning project
// (TestDispatchReportFileReadWithoutRepoNameIsAttributed pins that behavior).
// CHANGELOG.md's [0.3.12] "IPC Validation" entry used to present the removed
// f64535b guard (-32602 on a missing repo_name) as shipped behavior, which told
// integrators a payload the daemon accepts would be rejected. This guard fails
// if the falsified claim returns without a removal/supersession note.
func TestChangeLogRepoNameGuardDocumentedAsRemoved(t *testing.T) {
	raw, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md (go test runs this package from internal/ipc): %v", err)
	}
	const sectionStart = "## [0.3.12]"
	start := strings.Index(string(raw), sectionStart)
	if start < 0 {
		t.Fatalf("CHANGELOG.md has no %q section; update this guard if the section was renamed", sectionStart)
	}
	rest := string(raw)[start+len(sectionStart):]
	end := strings.Index(rest, "\n## ")
	if end < 0 {
		end = len(rest)
	}
	section := rest[:end]

	// A mention of the guard is only accurate alongside a note that it was
	// removed/superseded — the dispatch accepts a missing repo_name today.
	markers := []string{"removed", "superseded", "no longer", "inferred", "optional", "accepts"}
	for _, line := range strings.Split(section, "\n") {
		if !strings.Contains(line, "report_file_read") {
			continue
		}
		if strings.Contains(line, "repo_name") && strings.Contains(line, "32602") {
			documented := false
			for _, m := range markers {
				if strings.Contains(strings.ToLower(line), m) {
					documented = true
					break
				}
			}
			if !documented {
				t.Fatalf("CHANGELOG [0.3.12] presents the removed repo_name -32602 guard as shipped behavior without a removal note; the live dispatch accepts a missing repo_name (see TestDispatchReportFileReadWithoutRepoNameIsAttributed): %s",
					strings.TrimSpace(line))
			}
		}
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func seedOutputSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRun(db.RunRecord{RunID: "keep-me", AgentName: "fixture", ModelName: "gpt-4o", CostUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func outputCommand(source, output, format string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("db", source, "")
	cmd.Flags().String("out", output, "")
	cmd.Flags().String("format", format, "")
	cmd.Flags().String("repo", "fixture", "")
	return cmd
}

func assertOutputSourceIntact(t *testing.T, path string) {
	t.Helper()
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("source database is unreadable: %v", err)
	}
	defer store.Close()
	_, _, _, found, err := store.RunTotals("keep-me")
	if err != nil || !found {
		t.Fatalf("source run lost: found=%v err=%v", found, err)
	}
}

func TestCLIOutputsCannotOverwriteSourceDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		run          func(*cobra.Command, []string) error
	}{
		{"export", "json", runExport},
		{"report-json", "json", runReport},
		{"report-html", "html", runReport},
		{"report-markdown", "markdown", runReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("separate-output-control", func(t *testing.T) {
				source := seedOutputSource(t)
				output := filepath.Join(t.TempDir(), "report.out")
				if err := os.WriteFile(output, []byte("previous output"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := tc.run(outputCommand(source, output, tc.format), nil); err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(output); err != nil || info.Size() == 0 {
					t.Fatalf("control output missing: %v", err)
				}
				assertOutputSourceIntact(t, source)
				t.Log("CONTROL EXPECTED: separate output succeeds, source run preserved | ACTUAL: true")
			})
			for _, alias := range []string{"same-path", "cleaned-alias", "hard-link"} {
				t.Run(alias, func(t *testing.T) {
					source := seedOutputSource(t)
					output := source
					if alias == "cleaned-alias" {
						sub := filepath.Join(filepath.Dir(source), "sub")
						if err := os.Mkdir(sub, 0700); err != nil {
							t.Fatal(err)
						}
						output = sub + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(source)
					}
					if alias == "hard-link" {
						output = filepath.Join(filepath.Dir(source), "alias.out")
						if err := os.Link(source, output); err != nil {
							t.Fatalf("create hard-link fixture: %v", err)
						}
					}
					err := tc.run(outputCommand(source, output, tc.format), nil)
					t.Logf("EXPECTED: reject output alias of source database | ACTUAL: err=%v", err)
					if err == nil {
						t.Error("CLI overwrote its source database and reported success")
					}
					if err != nil && !strings.Contains(err.Error(), "source database") {
						t.Error("alias must be rejected with a portable source-database diagnostic")
					}
					assertOutputSourceIntact(t, source)
				})
			}
		})
	}
}

package mcp

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// Round 7 (proof-driven bug hunt): the MCP `check_guardrail` tool re-derives
// the guardrail verdict locally instead of using the engine's, and the two had
// DRIFTED. core.Engine.CheckGuardrail (internal/core/guardrails.go:252-268) has
// THREE tiers -- warn below 40, "Caution: ..." below 70, else safe -- while the
// tool's local copy had only two, so a file with health 40-69 was answered
// "Safe to modify." over MCP and
//
//	Caution: File health score is 68/100. Apply minimal localized diffs.
//
// over HTTP and IPC, for the very same file. 4 edits in the last 24h produce
// exactly that band: 100 - 4*8 = 68, and fragility only starts at 5 edits, so
// the engine does not warn there -- it cautions.
//
// MCP is the transport Claude Code, Cursor and the other bundled agents read,
// so the one surface that loses the caution tier is the one agents act on.
//
// PARITY CONTRACT pinned here: for one and the same file, the MCP tool and the
// engine must agree on `allowed` AND on the tier of the recommendation
// (blocked / warning / caution / safe).
//
// The engine is real and store-backed and the events are seeded through the
// production writer, so both surfaces read the same health -- the only variable
// left is each side's own tier logic. (A hand-built FileHealth reply cannot be
// used here: Engine.CheckGuardrail calls e.FileHealth on the embedded receiver,
// so an adapter that shadows FileHealth is invisible to the engine's own path.)

func newGuardrailEngine(t *testing.T, edits int, distinctSigs int) (*core.Engine, string) {
	t.Helper()
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	// Engine.Store() returns cfg.Store (engine.go:233-237) and Engine.FileHealth
	// short-circuits to HealthScore 100 when it is nil, so the engine needs a
	// real store for its guardrail tiers to be observable at all.
	store, err := db.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng := core.NewEngine(core.Config{Store: store})
	const path = "src/app.ts"
	now := time.Now().UTC()
	for i := 0; i < edits; i++ {
		sig := "function:app.ts::run"
		if distinctSigs > 1 && i%2 == 0 {
			sig = fmt.Sprintf("function:app.ts::helper%d", i)
		}
		if err := store.InsertEvent(db.EventRecord{
			EventID:    fmt.Sprintf("ev-%d", i),
			RepoName:   "probe",
			FilePath:   path,
			Signature:  sig,
			NodeType:   "function",
			Action:     "MODIFIED",
			BodyHash:   "hash",
			LOC:        3,
			StartLine:  1,
			EndLine:    3,
			OccurredAt: now,
		}); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}
	return eng, path
}

// guardrailData pulls the tool's structured `data` map out of a dispatch result.
func guardrailData(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	data, ok := res["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %#v", res["data"])
	}
	return data
}

// guardrailTier names the recommendation band. The two surfaces word the
// warning differently, so the tier -- not the exact sentence -- is the contract
// for that band; the caution band is worded identically and is compared exactly.
func guardrailTier(rec string) string {
	switch {
	case strings.Contains(rec, "BLOCKED"):
		return "blocked"
	case strings.Contains(rec, "GUARDRAIL WARNING"):
		return "warning"
	case strings.Contains(rec, "Caution"):
		return "caution"
	case rec == "Safe to modify.":
		return "safe"
	}
	return "unknown:" + rec
}

func TestCheckGuardrail_TierParityWithEngine(t *testing.T) {
	cases := []struct {
		name         string
		edits        int
		distinctSigs int
		wantHealth   int
		wantTier     string
	}{
		// 100 - 4*8 = 68, not fragile (fragility starts at 5 edits).
		{"caution band (4 edits in 24h)", 4, 1, 68, "caution"},
		// 100 - 4*8 - 5 (3+ signatures) = 63, still caution.
		{"caution band with signature spread", 4, 3, 63, "caution"},
		{"safe band (no churn)", 0, 1, 100, "safe"},
		// 100 - 10*8 = 20 and fragile (>= 5 edits): both surfaces must warn.
		{"warning band (heavy churn)", 10, 1, 20, "warning"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, path := newGuardrailEngine(t, tc.edits, tc.distinctSigs)

			// The engine's own answer, through the engine's own path.
			engineRes, err := eng.CheckGuardrail(path)
			if err != nil {
				t.Fatalf("engine CheckGuardrail: %v", err)
			}
			if engineRes.HealthScore != tc.wantHealth {
				t.Fatalf("engine HealthScore = %d, want %d (fixture drift)", engineRes.HealthScore, tc.wantHealth)
			}
			if gotTier := guardrailTier(engineRes.Recommendation); gotTier != tc.wantTier {
				t.Fatalf("engine tier = %q (%q), want %q", gotTier, engineRes.Recommendation, tc.wantTier)
			}

			// The MCP tool's answer for the same file, through dispatch.
			resp := dispatch(eng, toolCallReq(1, "check_guardrail", fmt.Sprintf(`{"file_path":%q}`, path)))
			data := guardrailData(t, resp)

			allowed, _ := data["allowed"].(bool)
			if allowed != engineRes.Allowed {
				t.Errorf("allowed diverges: engine=%v mcp=%v", engineRes.Allowed, allowed)
			}

			rec, _ := data["recommendation"].(string)
			gotTier, wantTier := guardrailTier(rec), guardrailTier(engineRes.Recommendation)
			if gotTier != wantTier {
				t.Errorf("recommendation tier diverges: engine=%q (%s), mcp=%q (%s)",
					engineRes.Recommendation, wantTier, rec, gotTier)
			}
			if tc.wantTier == "caution" && rec != engineRes.Recommendation {
				t.Errorf("caution text diverges: engine=%q, mcp=%q", engineRes.Recommendation, rec)
			}
		})
	}
}

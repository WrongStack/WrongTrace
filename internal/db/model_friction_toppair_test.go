package db

import (
	"testing"
	"time"
)

// TestModelFrictionMatrix_TopPairDeterministic verifies that when multiple
// cross-agent pairs have the same conflict count, TopFrictionPair is
// deterministically selected (first by conflict count, then alphabetically).
//
// Non-deterministic bug: edges were iterated in random Go map order (lines 1824-1827).
// When two pairs tie at max conflict count, the arbitrary map iteration made
// topPair unpredictable across calls.
func TestModelFrictionMatrix_TopPairDeterministic(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// Two cross-agent pairs, SAME conflict count (3 each).
	// Pair 1: MiniMax -> claude-sonnet-4
	// Pair 2: MiniMax -> claude-opus-4  (alphabetically after pair 1)
	// Expected TopFrictionPair: MiniMax -> claude-opus-4  (alphabetically first among tied)
	pairs := []struct {
		author, overwriter string
	}{
		{"MiniMax-M2.7-highspeed", "claude-sonnet-4-20250514"},
		{"MiniMax-M2.7-highspeed", "claude-opus-4-20250514"},
	}

	for pi, pair := range pairs {
		for j := 0; j < 3; j++ {
			sig := "func:Work" + itoa(pi) // same signature within a pair → chain LAG
			seedRun(t, s, RunRecord{RunID: "run-a-" + itoa(pi) + "-" + itoa(j), ModelName: pair.author, CreatedAt: now})
			seedEvent(t, s, "ev-a-"+itoa(pi)+"-"+itoa(j), "run-a-"+itoa(pi)+"-"+itoa(j), sig, "ADDED", now)
			seedRun(t, s, RunRecord{RunID: "run-b-" + itoa(pi) + "-" + itoa(j), ModelName: pair.overwriter, CreatedAt: now.Add(time.Second)})
			seedEvent(t, s, "ev-b-"+itoa(pi)+"-"+itoa(j), "run-b-"+itoa(pi)+"-"+itoa(j), sig, "MODIFIED", now.Add(time.Second))
		}
	}

	// Run ModelFrictionMatrix multiple times with the same data.
	// Without sorting, map iteration order is random — topPair can be either pair.
	// With sorting, topPair must always be the alphabetically-first author (MiniMax -> claude-opus).
	var firstResult string
	for i := 0; i < 5; i++ {
		report, err := s.ModelFrictionMatrix(200)
		if err != nil {
			t.Fatalf("ModelFrictionMatrix call %d: %v", i, err)
		}
		if report.TopFrictionPair == "" {
			t.Fatalf("call %d: TopFrictionPair is empty", i)
		}
		if i == 0 {
			firstResult = report.TopFrictionPair
			t.Logf("First result: %s", firstResult)
		} else if report.TopFrictionPair != firstResult {
			t.Fatalf("call %d: TopFrictionPair = %q, expected %q (non-deterministic)",
				i, report.TopFrictionPair, firstResult)
		}
	}

	// Both overwriters ARE seeded above, each ending with 1 cross-agent
	// collision, so this fixture is a genuine tie and the winner must be decided
	// by the documented alphabetical tie-break -- claude-opus sorts before
	// claude-sonnet. This assertion previously pinned claude-sonnet, which is
	// neither alphabetical nor stable: it was whichever pair the old in-loop
	// running max happened to reach first under "ORDER BY event_time DESC". That
	// contradicted this test's own comment above and passed only because the
	// fixture's insertion order favoured it; the loop below checks stability,
	// which a fixed-but-winner-wrong value also satisfies.
	// Note: TopFrictionPair includes "(N collisions)" in its formatted value.
	want := "MiniMax-M2.7-highspeed ➔ claude-opus-4-20250514 (1 collisions)"
	if firstResult != want {
		t.Fatalf("TopFrictionPair = %q; want %q (stable cross-agent winner)",
			firstResult, want)
	}
	t.Logf("Deterministic TopFrictionPair: %s", firstResult)
}

// itoa converts a small int to a string without importing strconv.
func itoa(i int) string {
	if i < 0 {
		return "-" + uitoa(-i)
	}
	if i == 0 {
		return "0"
	}
	return uitoa(i)
}

func uitoa(i int) string {
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

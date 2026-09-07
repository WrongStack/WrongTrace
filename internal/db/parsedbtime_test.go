package db

import (
	"path/filepath"
	"testing"
	"time"
)

// TestParseDBTime_FastPathFallsThroughOnImpossibleDates pins the round-37
// contract: the hand-rolled fast paths must agree exactly with the layouts
// fallback. time.Date NORMALIZES impossible dates (Feb 31 -> Mar 3, second 60
// -> next minute), which silently shifts corrupt or hand-inserted rows into
// the wrong FileHealth decay / Thrashing lookback window; the documented
// behavior is the zero time ("unknown") for anything the layouts reject.
func TestParseDBTime_FastPathFallsThroughOnImpossibleDates(t *testing.T) {
	utc := func(s string) time.Time {
		tt, err := time.Parse(time.DateTime, s)
		if err != nil {
			t.Fatalf("test bug: %s: %v", s, err)
		}
		return tt.UTC()
	}
	cases := []struct {
		in       string
		want     time.Time
		wantZero bool
	}{
		// Impossible dates: must fall through and parse as unknown (zero).
		{"2026-02-31 10:00:00", time.Time{}, true},
		{"2023-02-29 08:30:00", time.Time{}, true}, // non-leap year
		{"2026-04-31 12:00:00", time.Time{}, true},
		{"2026-01-01 10:00:60", time.Time{}, true}, // second=60 rejected by the layout
		// Valid timestamps: must keep parsing exactly.
		{"2026-09-06 10:00:00", utc("2026-09-06 10:00:00"), false},
		{"2024-02-29 12:00:00", utc("2024-02-29 12:00:00"), false},  // leap day accepted
		{"2026-01-01 10:00:59", utc("2026-01-01 10:00:59"), false},  // second=59 boundary
		{"2100-12-31 23:59:59", utc("2100-12-31 23:59:59"), false},  // year ceiling boundary
		{"2026-09-06T10:00:00Z", utc("2026-09-06 10:00:00"), false}, // 20-char fast path
		// Out-of-fast-path-range years: fast path skips, layouts fallback parses.
		{"2101-01-01 00:00:00", utc("2101-01-01 00:00:00"), false},
		{"1969-12-31 23:59:59", utc("1969-12-31 23:59:59"), false},
		// Fallback layouts still honored (pre-existing behavior).
		{"2026-09-06 10:00:05.123-07:00", mustParseOffset("2026-09-06 10:00:05.123-07:00"), false},
		{"1969-12-31 23:59:59", utc("1969-12-31 23:59:59"), false}, // pre-1970: fast path skips, layout parses
		// Unknown remains unknown.
		{"not-a-timestamp", time.Time{}, true},
		{"", time.Time{}, true},
	}
	for _, c := range cases {
		got := parseDBTime(c.in)
		if c.wantZero {
			if !got.IsZero() {
				t.Errorf("parseDBTime(%q) = %s, want zero time (unknown)", c.in, got.Format(time.RFC3339))
			}
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseDBTime(%q) = %s, want %s", c.in, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}

// TestRecentEvents_HandInsertedImpossibleTimestampIsUnknown drives the real
// scan path (RecentEvents -> scan helper -> parseDBTime) over a raw-inserted
// row, the "older build or by hand" input class the doc comment anticipates.
func TestRecentEvents_HandInsertedImpossibleTimestampIsUnknown(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "parsedbtime.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insertRaw := func(id, eventTime string) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO code_node_events (event_id, repo_name, file_path, node_signature, node_type, action, lines_of_code, event_time)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "proof-repo", "proof/file.go", "proof.Func", "function", "MODIFIED", 10, eventTime,
		); err != nil {
			t.Fatalf("raw insert %s: %v", id, err)
		}
	}
	insertRaw("ev-impossible", "2026-02-31 10:00:00")
	insertRaw("ev-valid", "2026-09-06 10:00:00")

	events, err := st.RecentEvents(50)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	byID := make(map[string]EventRecord, len(events))
	for _, e := range events {
		byID[e.EventID] = e
	}

	imp, ok := byID["ev-impossible"]
	if !ok {
		t.Fatalf("impossible-time row missing from RecentEvents")
	}
	if !imp.OccurredAt.IsZero() {
		t.Errorf("impossible event_time parsed as %s, want zero time (unknown)",
			imp.OccurredAt.Format(time.RFC3339))
	}

	val, ok := byID["ev-valid"]
	if !ok {
		t.Fatalf("valid-time row missing from RecentEvents")
	}
	want, _ := time.Parse(time.DateTime, "2026-09-06 10:00:00")
	if !val.OccurredAt.Equal(want) {
		t.Errorf("valid event_time parsed as %s, want %s",
			val.OccurredAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// mustParseOffset parses with the fractional+offset layout from dbTimeLayouts.
func mustParseOffset(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", s)
	if err != nil {
		panic("test bug: " + err.Error())
	}
	return t
}

package mcp

import (
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// limitRecordingSink records the limit argument the dispatch layer hands to
// the sink. The plain fakeSink discards it, which is exactly how the
// unbounded get_file_diff_history limit survived: no observation, no test.
type limitRecordingSink struct {
	*fakeSink

	eventsCalls     int
	eventsLimit     int
	fileEventsCalls int
	fileEventsLimit int
	fileEventsPath  string
}

func (s *limitRecordingSink) GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error) {
	s.eventsCalls++
	s.eventsLimit = limit
	return nil, nil
}

func (s *limitRecordingSink) GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error) {
	s.fileEventsCalls++
	s.fileEventsLimit = limit
	s.fileEventsPath = filePath
	return nil, nil
}

// Regression (round 31): get_file_diff_history forwarded the client-supplied
// limit to the engine sink unclamped. The HTTP surface bounds the identical
// input at maxRecentEventsLimit = 1000 ("an unbounded value would let one
// request pin megabytes of records in memory"), and the store only defaults
// limit <= 0 to 50 — so a hallucinated limit of 999999999 from an MCP agent
// reached `LIMIT ?` verbatim and materialized the whole code_node_events
// table into one stdio response. The dispatch layer now clamps at
// maxMCPHistoryLimit while legitimate values pass through unchanged.
func TestGetFileDiffHistory_HostileLimitIsClamped(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "file-scoped", args: `{"file_path":"internal/watcher/watcher.go","limit":999999999}`},
		{name: "repo-wide", args: `{"limit":999999999}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &limitRecordingSink{fakeSink: &fakeSink{}}
			resp := dispatch(sink, toolCallReq(1, "get_file_diff_history", tc.args))
			if resp.Error != nil {
				t.Fatalf("unexpected rpc error: %+v", resp.Error)
			}
			if sink.fileEventsCalls != 1 && tc.name == "file-scoped" {
				t.Fatalf("GetRecentFileEvents not called: calls=%d", sink.fileEventsCalls)
			}
			if sink.eventsCalls != 1 && tc.name == "repo-wide" {
				t.Fatalf("GetRecentEvents not called: calls=%d", sink.eventsCalls)
			}
			got := sink.fileEventsLimit
			if tc.name == "repo-wide" {
				got = sink.eventsLimit
			}
			if got != maxMCPHistoryLimit {
				t.Fatalf("limit reached the sink as %d, want clamped %d", got, maxMCPHistoryLimit)
			}
		})
	}
}

func TestGetFileDiffHistory_LegitimateLimitPassesThrough(t *testing.T) {
	sink := &limitRecordingSink{fakeSink: &fakeSink{}}
	resp := dispatch(sink, toolCallReq(1, "get_file_diff_history", `{"file_path":"a.go","limit":25}`))
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	if sink.fileEventsLimit != 25 {
		t.Fatalf("legitimate limit altered: got %d, want 25", sink.fileEventsLimit)
	}

	sink2 := &limitRecordingSink{fakeSink: &fakeSink{}}
	if resp := dispatch(sink2, toolCallReq(2, "get_file_diff_history", `{"limit":999}`)); resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	} else if sink2.eventsLimit != 999 {
		t.Fatalf("in-cap limit altered: got %d, want 999", sink2.eventsLimit)
	}
}

// TestGetFileDiffHistory_LimitBoundaries pins the cap edge and the
// legacy default: exactly-at-cap stays put, one over clamps, and the
// absent/zero/negative forms still fall back to the historical default of 20.
func TestGetFileDiffHistory_LimitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       string
		fileScoped bool
		want       int
	}{
		{name: "exactly at cap", args: `{"limit":1000}`, want: 1000},
		{name: "one over cap", args: `{"limit":1001}`, want: 1000},
		{name: "file-scoped at cap", args: `{"file_path":"a.go","limit":5000}`, fileScoped: true, want: 1000},
		{name: "absent", args: `{"file_path":"a.go"}`, fileScoped: true, want: 20},
		{name: "zero", args: `{"limit":0}`, want: 20},
		{name: "negative", args: `{"limit":-5}`, want: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &limitRecordingSink{fakeSink: &fakeSink{}}
			if resp := dispatch(sink, toolCallReq(1, "get_file_diff_history", tc.args)); resp.Error != nil {
				t.Fatalf("unexpected rpc error: %+v", resp.Error)
			}
			got := sink.eventsLimit
			if tc.fileScoped {
				got = sink.fileEventsLimit
			}
			if got != tc.want {
				t.Fatalf("limit = %d, want %d", got, tc.want)
			}
		})
	}
}

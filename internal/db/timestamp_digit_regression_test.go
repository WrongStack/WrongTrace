package db

import (
	"fmt"
	"testing"
	"time"
)

func TestParseDBTime_RejectsNondecimalFields(t *testing.T) {
	for _, base := range []string{"2026-10-01 00:00:00", "2026-10-01T00:00:00Z"} {
		for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
			for _, c := range []byte{':', '/', 'x'} {
				b := []byte(base)
				b[i] = c
				if got := parseDBTime(string(b)); !got.IsZero() {
					t.Errorf("parseDBTime(%q)=%v; want unknown", string(b), got)
				}
			}
		}
		if got := parseDBTime(base); got.IsZero() {
			t.Errorf("valid control %q became unknown", base)
		}
	}
}

func TestRecentEvents_NondecimalTimestampIsUnknown(t *testing.T) {
	s := openAuditStore(t)
	inputs := []string{"2026-10-01 00:00:00", "2026-0:-01 00:00:00", "197:-01-01T00:00:00Z"}
	for i, input := range inputs {
		if _, err := s.DB().Exec(`INSERT INTO code_node_events(event_id,repo_name,file_path,node_signature,node_type,action,event_time) VALUES(?, 'audit', 'file.go', 'fn', 'function', 'MODIFIED', ?)`, fmt.Sprint(i), input); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.RecentEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d; want3", len(rows))
	}
	for _, row := range rows {
		if row.EventID == "0" {
			want, _ := time.Parse(time.DateTime, inputs[0])
			if !row.OccurredAt.Equal(want) {
				t.Fatal("valid control changed")
			}
		} else if !row.OccurredAt.IsZero() {
			t.Errorf("invalid row %s became %v", row.EventID, row.OccurredAt)
		}
	}
}

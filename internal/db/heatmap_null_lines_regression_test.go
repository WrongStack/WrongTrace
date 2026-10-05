package db

import (
	"fmt"
	"reflect"
	"testing"
)

func TestGetFileReadHeatmap_NullableBoundaries(t *testing.T) {
	s := openTestStore(t)
	for _, tc := range []struct {
		id         string
		start, end any
	}{
		{"null-start", nil, 4}, {"zero-start", 0, 4}, {"null-end", 2, nil}, {"ordinary", 2, 4}, {"null-both", nil, nil}, {"zero-both", 0, 0},
	} {
		if _, err := s.DB().Exec(`INSERT INTO file_read_events(read_id,repo_name,file_path,agent_name,model_name,provider,tool_name,start_line,end_line,read_time) VALUES(?,'audit','nullable.go','a','m','p','read_file',?,?,'2026-01-01 00:00:00')`, tc.id, tc.start, tc.end); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 0; pass < 2; pass++ {
		heatmap, err := s.GetFileReadHeatmap("nullable.go")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, h := range heatmap {
			got[fmt.Sprintf("%d..%d", h.StartLine, h.EndLine)] = h.ReadCount
		}
		want := map[string]int{"0..4": 2, "2..0": 1, "2..4": 1}
		if len(heatmap) != 3 || !reflect.DeepEqual(got, want) {
			t.Fatalf("heatmap=%+v; want3 normalized ranges %v", heatmap, want)
		}
	}
	stats, err := s.GetFileReadStats("nullable.go")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalReads != 6 {
		t.Fatalf("stats.TotalReads=%d; want6", stats.TotalReads)
	}
	empty, err := s.GetFileReadHeatmap("absent.go")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

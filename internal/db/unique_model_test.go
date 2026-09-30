package db

import (
	"testing"
	"time"
)

func TestGetFileReadStats_EmptyModelIsNotUnique(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.InsertReadEvent(FileReadRecord{
		ReadID: "unnamed", FilePath: "pkg/unnamed.go", Provider: "provider-a", ReadTime: now,
	}); err != nil {
		t.Fatalf("insert unnamed read: %v", err)
	}
	stats, err := s.GetFileReadStats("pkg/unnamed.go")
	if err != nil {
		t.Fatalf("get unnamed stats: %v", err)
	}
	if stats.UniqueModels != 0 || len(stats.ModelBreakdown) != 0 {
		t.Fatalf("unnamed stats: UniqueModels=%d ModelBreakdown=%v, want 0/empty", stats.UniqueModels, stats.ModelBreakdown)
	}

	if err := s.InsertReadEvent(FileReadRecord{
		ReadID: "named", FilePath: "pkg/named.go", ModelName: "gpt-4o", Provider: "provider-a", ReadTime: now,
	}); err != nil {
		t.Fatalf("insert named read: %v", err)
	}
	stats, err = s.GetFileReadStats("pkg/named.go")
	if err != nil {
		t.Fatalf("get named stats: %v", err)
	}
	if stats.UniqueModels != 1 || stats.ModelBreakdown["gpt-4o"] != 1 {
		t.Fatalf("named stats: UniqueModels=%d ModelBreakdown=%v, want 1/gpt-4o=1", stats.UniqueModels, stats.ModelBreakdown)
	}
}

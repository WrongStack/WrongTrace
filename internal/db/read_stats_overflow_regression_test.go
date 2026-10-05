package db

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestFileReadStatsRejectsUnrepresentableTotals(t *testing.T) {
	s := openTestStore(t)
	stamp := time.Unix(1700000000, 0)
	for i := 0; i < 2; i++ {
		if e := s.InsertReadEvent(FileReadRecord{ReadID: fmt.Sprintf("control%d", i), FilePath: "control.go", PromptTokens: 7, CachedTokens: 3, LinesReadCount: 2, CostUSD: 0.5, ReadTime: stamp}); e != nil {
			t.Fatal(e)
		}
	}
	control, e := s.GetFileReadStats("control.go")
	if e != nil || control.TotalPromptTokens != 14 || control.TotalCachedTokens != 6 || control.TotalLinesRead != 4 || control.TotalCostUSD != 1 {
		t.Fatal("ordinary control")
	}
	fmt.Println("CONTROL EXPECTED: prompt14/cache6/lines4/cost1 | ACTUAL: true")
	failed := false
	for _, field := range []string{"prompt", "cached", "lines", "cost"} {
		for i := 0; i < 2; i++ {
			r := FileReadRecord{ReadID: fmt.Sprintf("%s%d", field, i), FilePath: field + ".go", ReadTime: stamp}
			switch field {
			case "prompt":
				r.PromptTokens = 1
				if i == 0 {
					r.PromptTokens = math.MaxInt64
				}
			case "cached":
				r.CachedTokens = 1
				if i == 0 {
					r.CachedTokens = math.MaxInt64
				}
			case "lines":
				r.LinesReadCount = 1
				if i == 0 {
					r.LinesReadCount = int(^uint(0) >> 1)
				}
			case "cost":
				r.CostUSD = math.MaxFloat64
			}
			if e := s.InsertReadEvent(r); e != nil {
				t.Fatal(e)
			}
		}
		v, e := s.GetFileReadStats(field + ".go")
		fmt.Printf("%s EXPECTED: explicit aggregate overflow error, no wrapped/nonfinite successful totals | ACTUAL: err%v prompt%d cached%d lines%d cost%g\n", field, e, v.TotalPromptTokens, v.TotalCachedTokens, v.TotalLinesRead, v.TotalCostUSD)
		failed = failed || e == nil
		if math.IsInf(v.TotalCostUSD, 0) || math.IsNaN(v.TotalCostUSD) || v.TotalLinesRead < 0 || v.TotalPromptTokens < 0 || v.TotalCachedTokens < 0 {
			t.Fatal("error returned already-corrupt partial totals")
		}
	}
	empty, e := s.GetFileReadStats("missing.go")
	if e != nil || empty.TotalReads != 0 {
		t.Fatal("empty boundary")
	}
	if e := s.InsertReadEvent(FileReadRecord{ReadID: "exact", FilePath: "exact.go", PromptTokens: math.MaxInt64, CachedTokens: math.MaxInt64, LinesReadCount: int(^uint(0) >> 1), CostUSD: math.MaxFloat64, ReadTime: stamp}); e != nil {
		t.Fatal(e)
	}
	exact, e := s.GetFileReadStats("exact.go")
	if e != nil || exact.TotalPromptTokens != math.MaxInt64 || exact.TotalCachedTokens != math.MaxInt64 || exact.TotalLinesRead != int(^uint(0)>>1) || exact.TotalCostUSD != math.MaxFloat64 {
		t.Fatal("exact maximum boundary rejected", e)
	}
	for i, tokens := range []int64{math.MinInt64, -1} {
		if e := s.InsertReadEvent(FileReadRecord{ReadID: fmt.Sprintf("negative%d", i), FilePath: "negative.go", PromptTokens: tokens, ReadTime: stamp}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.GetFileReadStats("negative.go"); e == nil {
		t.Fatal("negative underflow not reported")
	}
	for i, cost := range []float64{math.MaxFloat64, -math.MaxFloat64} {
		if e := s.InsertReadEvent(FileReadRecord{ReadID: fmt.Sprintf("cancel%d", i), FilePath: "cancel.go", CostUSD: cost, ReadTime: stamp}); e != nil {
			t.Fatal(e)
		}
	}
	if canceled, e := s.GetFileReadStats("cancel.go"); e != nil || canceled.TotalCostUSD != 0 {
		t.Fatal("representable cancellation changed")
	}
	if failed {
		t.Fatal("PROBLEM CONFIRMED")
	}
}

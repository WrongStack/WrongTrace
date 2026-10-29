package proxy

import (
	"strings"
	"testing"
)

// A tail that continues a newline-terminated head starts with a whole record
// and must be kept whole; a tail that wrapped, or follows a head cut mid-line,
// starts with a fragment that must be dropped.
func TestStreamAnalysisPayload_KeepsWholeRecordAtSeam(t *testing.T) {
	usage := "data: {\"usage\":{\"prompt_tokens\":4321,\"completion_tokens\":8765}}\n\n"
	cases := []struct {
		name      string
		head      string
		tail      string
		overwrote bool
		wantUsage bool
		wantFrag  bool
	}{
		{"contiguous, head ends on newline", "data: {\"x\":1}\n\n", usage, false, true, false},
		{"contiguous, head ends mid-line", "data: {\"x\":1}\n\ndata: {\"us", "age\":1}\n\n" + usage, false, true, false},
		{"tail wrapped", "data: {\"x\":1}\n\n", "fragment\n" + usage, true, true, false},
		{"empty head", "", usage, false, true, false},
	}
	for _, c := range cases {
		got := string(streamAnalysisPayload([]byte(c.head), []byte(c.tail), c.overwrote))
		if strings.Contains(got, `"prompt_tokens":4321`) != c.wantUsage {
			t.Errorf("%s: usage record kept=%v, want %v: %q", c.name, !c.wantUsage, c.wantUsage, got)
		}
		if strings.Contains(got, "fragment") || strings.Contains(got, `age":1}`) {
			t.Errorf("%s: leading fragment was not dropped: %q", c.name, got)
		}
	}

	b := newCappedTailBuffer(8)
	b.Write([]byte("12345678"))
	if b.Overwrote() {
		t.Error("ring filled exactly to its limit reported overwritten bytes")
	}
	b.Write([]byte("9"))
	if !b.Overwrote() {
		t.Error("ring that discarded a byte did not report it")
	}
}

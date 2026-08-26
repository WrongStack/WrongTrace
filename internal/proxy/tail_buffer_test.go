package proxy

import (
	"bytes"
	"testing"
)

func BenchmarkCappedTailCapture(b *testing.B) {
	chunk := bytes.Repeat([]byte("x"), 4096)
	const (
		streamBytes = 16 * 1024 * 1024
		tailBytes   = 256 * 1024
	)
	b.ReportAllocs()
	b.SetBytes(streamBytes)
	for i := 0; i < b.N; i++ {
		tail := newCappedTailBuffer(tailBytes)
		for written := 0; written < streamBytes; written += len(chunk) {
			tail.Write(chunk)
		}
		if got := tail.Bytes(); len(got) != tailBytes {
			b.Fatalf("tail length = %d, want %d", len(got), tailBytes)
		}
	}
}

func TestCappedTailBufferRetainsNewestBytes(t *testing.T) {
	tail := newCappedTailBuffer(8)
	for _, chunk := range [][]byte{[]byte("abc"), []byte("defghi"), []byte("jkl")} {
		tail.Write(chunk)
	}
	if got := string(tail.Bytes()); got != "efghijkl" {
		t.Fatalf("tail = %q, want %q", got, "efghijkl")
	}

	tail.Write([]byte("0123456789"))
	if got := string(tail.Bytes()); got != "23456789" {
		t.Fatalf("oversized tail = %q, want %q", got, "23456789")
	}
}

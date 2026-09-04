package ingest

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzRuneSafeTruncate exercises the truncation helper on arbitrary strings.
//
// It walks a string by byte index while counting runes and slices at that
// index -- exactly the shape that produces a panic or a mangled rune on
// multi-byte or invalid UTF-8. The strings it truncates come from agent
// transcripts and LLM responses, so they routinely contain emoji, CJK text, and
// bodies cut off mid-rune by a dropped connection.
func FuzzRuneSafeTruncate(f *testing.F) {
	for _, s := range []string{
		"", "a", "hello world",
		"héllo wörld", "日本語のテキスト", "emoji 🎉🎉🎉 here",
		"\xff\xfe invalid utf8", "a\xc3", // truncated multi-byte sequence
		strings.Repeat("é", 100),
	} {
		for _, n := range []int{-1, 0, 1, 3, 100} {
			f.Add(s, n)
		}
	}

	f.Fuzz(func(t *testing.T, s string, maxRunes int) {
		got := runeSafeTruncate(s, maxRunes)

		if maxRunes <= 0 {
			if got != "" {
				t.Fatalf("runeSafeTruncate(%q, %d) = %q, want empty", s, maxRunes, got)
			}
			return
		}

		// Truncation must never invent content, and must never produce more
		// runes than asked for (plus the ellipsis it appends).
		if got != s {
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("truncated output %q lacks the ellipsis marker (input %q, max %d)",
					got, s, maxRunes)
			}
			body := strings.TrimSuffix(got, "…")
			if !strings.HasPrefix(s, body) {
				t.Fatalf("truncated output is not a prefix of the input:\n  in:  %q\n  out: %q", s, got)
			}
			// The kept portion must not exceed the requested rune budget. Only
			// meaningful for valid UTF-8: RuneCountInString counts each invalid
			// byte as one rune, which is also how the function counts them.
			if n := utf8.RuneCountInString(body); n > maxRunes {
				t.Fatalf("kept %d runes, budget was %d (in %q)", n, maxRunes, s)
			}
		}
	})
}

// FuzzNormalizeModelName covers the model-name canonicalizer. Its input is
// whatever string an agent transcript put in a model field, and its output is
// used as a map key and persisted, so a panic here breaks ingestion of the
// whole file.
func FuzzNormalizeModelName(f *testing.F) {
	for _, s := range []string{
		"", "gpt-4", "claude-3-5-sonnet-20241022", "anthropic/claude-3.7-sonnet",
		"GPT-4O", "  spaced  ", "/", "//", "a/b/c",
		"\xff\xfe", strings.Repeat("x", 1000),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		_ = normalizeModelName(raw)
	})
}

// FuzzDetectAgentFromPath: agent attribution runs on every discovered
// transcript path, including paths from other tools' directories that this
// code has never seen.
func FuzzDetectAgentFromPath(f *testing.F) {
	for _, s := range []string{
		"", "/", `\`, `C:\Users\x\.claude\projects\a.jsonl`,
		"/home/u/.cursor/sessions/x.json", "relative/path.jsonl",
		strings.Repeat("/", 500), "\xff\xfe/\xff",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, p string) {
		_ = detectAgentFromPath(p)
	})
}

package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// captureServer records the JSON body of the single request it receives.
func captureServer(t *testing.T) (*httptest.Server, <-chan map[string]interface{}) {
	t.Helper()
	got := make(chan map[string]interface{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		got <- m
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func firstItem(t *testing.T, body map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	items, ok := body[key].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("body[%q] = %v; want one item", key, body[key])
	}
	return items[0].(map[string]interface{})
}

// Alert text carries agent-controlled strings (paths, lock reasons). Slack
// parses "<...>" in text as mentions and masked links, so it must arrive
// escaped; plain text must arrive unchanged.
func TestSendSlack_EscapesControlSequences(t *testing.T) {
	d := NewDispatcher(Config{Timeout: time.Second})
	cases := []struct{ in, want string }{
		{"blocked: src/app.go (release freeze)", "blocked: src/app.go (release freeze)"},
		{"(<!channel> re-auth at <https://evil.example|console> & retry)",
			"(&lt;!channel&gt; re-auth at &lt;https://evil.example|console&gt; &amp; retry)"},
		{"&lt; already", "&amp;lt; already"},
		{"", ""},
	}
	for _, c := range cases {
		srv, got := captureServer(t)
		if err := d.sendSlack(context.Background(), srv.URL, Payload{EventType: EventGuardrailBlock, Message: c.in}); err != nil {
			t.Fatalf("sendSlack: %v", err)
		}
		att := firstItem(t, <-got, "attachments")
		if att["text"] != c.want {
			t.Errorf("text for %q = %q; want %q", c.in, att["text"], c.want)
		}
	}
}

// Discord rejects an embed description over 4096 characters, dropping the
// alert; it must be cut to fit, on a rune boundary, and short text kept whole.
func TestSendDiscord_DescriptionFitsLimit(t *testing.T) {
	d := NewDispatcher(Config{Timeout: time.Second})
	for _, msg := range []string{
		"short alert",
		strings.Repeat("x", discordDescriptionMax),
		strings.Repeat("é", discordDescriptionMax+1),
		strings.Repeat("y", 5000),
	} {
		srv, got := captureServer(t)
		if err := d.sendDiscord(context.Background(), srv.URL, Payload{EventType: EventGuardrailBlock, Message: msg}); err != nil {
			t.Fatalf("sendDiscord: %v", err)
		}
		desc, _ := firstItem(t, <-got, "embeds")["description"].(string)
		n := utf8.RuneCountInString(desc)
		if n > discordDescriptionMax || !utf8.ValidString(desc) {
			t.Errorf("description of %d runes (valid=%v) exceeds the limit", n, utf8.ValidString(desc))
		}
		if utf8.RuneCountInString(msg) <= discordDescriptionMax && desc != msg {
			t.Errorf("message within the limit was altered (%d -> %d runes)", utf8.RuneCountInString(msg), n)
		}
	}
}

// Discord renders markdown in embed descriptions: a masked link in a path
// or lock reason must arrive inert.
func TestSendDiscord_EscapesMarkdown(t *testing.T) {
	d := NewDispatcher(Config{Timeout: time.Second})
	srv, got := captureServer(t)
	msg := "locked file: src/[Approve unlock](https://evil.example) **now**"
	if err := d.sendDiscord(context.Background(), srv.URL, Payload{EventType: EventGuardrailBlock, Message: msg}); err != nil {
		t.Fatalf("sendDiscord: %v", err)
	}
	desc, _ := firstItem(t, <-got, "embeds")["description"].(string)
	want := `locked file: src/\[Approve unlock\]\(https://evil.example\) \*\*now\*\*`
	if desc != want {
		t.Errorf("description = %q; want %q", desc, want)
	}
}

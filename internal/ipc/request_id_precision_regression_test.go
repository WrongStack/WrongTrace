package ipc

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Decode response IDs as raw JSON so the test oracle cannot lose the same
// integer precision that the server must preserve.
func TestHandleConn_RequestIDRetainsNumericPrecision(t *testing.T) {
	_, path := startLiveServer(t, nil)
	c := newClient(t, path)
	for pass := 0; pass < 2; pass++ {
		for _, id := range []string{"0", "42", "9007199254740991", "9007199254740993", "-9007199254740993", "18446744073709551615", "1e100", `"string-id"`, "null"} {
			c.send(t, fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"method\":\"ping\",\"id\":%s}\n", id))
			if err := c.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			line, err := c.r.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]json.RawMessage
			if err = json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			if got := string(response["id"]); got != id {
				t.Fatalf("pass%d requestID%s → replyID%s", pass, id, got)
			}
			if _, hasError := response["error"]; hasError {
				t.Fatalf("ping error: %s", line)
			}
		}
	}
}

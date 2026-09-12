package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAddProject_wrongType_returnsNameError is a same-package regression test
// that bypasses any HTTP-layer test caching. It calls the handler function directly.
func TestAddProject_wrongType_returnsNameError(t *testing.T) {
	handlers := &Handlers{Engine: nil}

	body := []byte(`{"name": 123, "path": 456}`)
	r := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handlers.AddProject(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	errMsg := resp["error"]
	t.Logf("error message: %q", errMsg)

	// The name field is wrong-type first (float64 vs string), so its error must surface.
	if errMsg != "invalid request body: expected string for field 'name', got float64" {
		t.Errorf("wrong error message: got %q, want %q",
			errMsg,
			"invalid request body: expected string for field 'name', got float64")
	}
}

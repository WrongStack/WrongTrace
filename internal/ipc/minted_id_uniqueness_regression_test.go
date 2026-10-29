package ipc

import "testing"

// IDs the server makes up must differ between requests. They were derived
// from time.Now().UnixNano(), which is coarse on Windows: a burst of reports
// shared one ID, so reads were dropped and runs overwrote each other while
// every caller was told ok.
func TestDispatch_MintedRunIDsAreUnique(t *testing.T) {
	sink := &fakeSink{}
	srv := &Server{cfg: Config{Engine: sink}}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		resp := srv.dispatch(&Request{
			Method: "report_telemetry",
			Params: map[string]interface{}{"model": "m", "prompt_tokens": float64(10)},
			ID:     i,
		})
		if resp.Error != nil {
			t.Fatalf("dispatch: %v", resp.Error)
		}
		id := sink.gotReport.RunID
		if id == "" || seen[id] {
			t.Fatalf("report %d minted run_id %q, already used", i, id)
		}
		seen[id] = true
	}
	// Control: a caller-supplied run_id is kept as given.
	srv.dispatch(&Request{Method: "report_telemetry", Params: map[string]interface{}{"run_id": "mine", "model": "m"}, ID: 1})
	if sink.gotReport.RunID != "mine" {
		t.Fatalf("caller run_id replaced: %q", sink.gotReport.RunID)
	}
}

func TestMintID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := mintID("read")
		if seen[id] {
			t.Fatalf("mintID repeated %q after %d calls", id, i)
		}
		seen[id] = true
	}
}

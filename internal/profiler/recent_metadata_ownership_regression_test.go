package profiler

import "testing"

func TestRecentMetadataOwnership(t *testing.T) {
	for _, boundary := range []string{"input", "returned", "recent", "callback"} {
		t.Run(boundary, func(t *testing.T) {
			var callback TraceEvent
			c := NewCollector(Config{OnTrace: func(ev TraceEvent) { callback = ev }})
			metadata := map[string]interface{}{"count": int64(1), "nested": map[string]interface{}{"count": 1}, "array": []interface{}{map[string]interface{}{"count": 1}}}
			ev, err := c.IngestReport(ProfilerReportPayload{Metadata: metadata})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := c.Recent(1)
			target := metadata
			switch boundary {
			case "returned":
				target = ev.Metadata
			case "recent":
				target = snapshot[0].Metadata
			case "callback":
				target = callback.Metadata
			}
			gate, done := make(chan struct{}), make(chan struct{})
			go func() {
				<-gate
				target["count"] = int64(99)
				target["nested"].(map[string]interface{})["count"] = 99
				target["array"].([]interface{})[0].(map[string]interface{})["count"] = 99
				close(done)
			}()
			close(gate)
			<-done
			got, _ := c.Recent(1)
			m := got[0].Metadata
			if m["count"] != int64(1) || m["nested"].(map[string]interface{})["count"] != 1 || m["array"].([]interface{})[0].(map[string]interface{})["count"] != 1 {
				t.Fatal("external mutation changed ring")
			}
		})
	}
	for _, m := range []map[string]interface{}{nil, {}} {
		got := cloneTraceMetadata(m)
		if (m == nil) != (got == nil) {
			t.Fatal("nil/empty map shape changed")
		}
	}
}

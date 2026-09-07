package db

import (
	"path/filepath"
	"testing"
)

// TestProfilerHotspots_ToleratesNullColumns pins the round-45 contract:
// file_path is schema-nullable in runtime_traces and is grouped/returned raw,
// AVG/MAX(duration_ms) yield NULL when a group's rows carry only NULL
// durations, and MAX(timestamp) yields NULL for a NULL-timestamp group. A
// schema-legal row carrying any of those NULLs must not abort the whole scan —
// before the fix one row made ProfilerHotspots return "scan hotspot:
// converting NULL to string is unsupported", and cmd/wrongtrace report
// swallowed the error, silently shipping the report without hotspots, exit 0.
func TestProfilerHotspots_ToleratesNullColumns(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nullhotspot.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// NULL-poisoned group: non-empty node_signature passes the WHERE, but
	// file_path, duration_ms and timestamp are all explicitly NULL.
	if _, err := st.DB().Exec(
		`INSERT INTO runtime_traces (trace_id, service_name, profiler_type, node_signature, file_path, duration_ms, status_code, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"hs-null", "proofsvc", "custom", "func:H1", nil, nil, nil, nil,
	); err != nil {
		t.Fatalf("raw insert hs-null: %v", err)
	}
	// Healthy control group.
	if _, err := st.DB().Exec(
		`INSERT INTO runtime_traces (trace_id, service_name, profiler_type, node_signature, file_path, duration_ms, status_code, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"hs-ctrl", "proofsvc", "custom", "func:H2", "proof/x.go", 5.0, 200, "2026-09-06 10:00:01",
	); err != nil {
		t.Fatalf("raw insert hs-ctrl: %v", err)
	}

	hotspots, err := st.ProfilerHotspots(10)
	if err != nil {
		t.Fatalf("ProfilerHotspots must survive schema-legal NULL columns: %v", err)
	}

	byNode := make(map[string]ProfilerHotspotRow, len(hotspots))
	for _, h := range hotspots {
		byNode[h.NodeSignature] = h
	}

	nullGroup, ok := byNode["func:H1"]
	if !ok {
		t.Fatalf("NULL group missing from ProfilerHotspots results")
	}
	if nullGroup.FilePath != "" {
		t.Errorf("NULL file_path read back as %q, want empty string", nullGroup.FilePath)
	}
	if nullGroup.AvgDurationMs != 0 || nullGroup.MaxDurationMs != 0 {
		t.Errorf("NULL durations read back as avg=%v max=%v, want 0/0", nullGroup.AvgDurationMs, nullGroup.MaxDurationMs)
	}
	if !nullGroup.LastSeen.IsZero() {
		t.Errorf("NULL last_seen read back as %v, want the zero time (unknown)", nullGroup.LastSeen)
	}

	ctrl, ok := byNode["func:H2"]
	if !ok {
		t.Fatalf("control group missing from ProfilerHotspots results")
	}
	if ctrl.AvgDurationMs != 5.0 {
		t.Errorf("control avg duration read back as %v, want 5.0", ctrl.AvgDurationMs)
	}
	if ctrl.LastSeen.IsZero() {
		t.Errorf("control last_seen read back as zero time, want the parsed timestamp")
	}
}

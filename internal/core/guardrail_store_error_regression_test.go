package core

import (
	"strings"
	"testing"
	"time"
)

func TestCheckGuardrail_HealthStoreFailureIsNotAllowed(t *testing.T) {
	e, s := newTestEngine(t)
	t.Cleanup(e.Close)
	e.webhooks = nil // This local guardrail test must never dispatch external notifications.
	control, err := e.CheckGuardrail("unknown.go")
	if err != nil || !control.Allowed || control.HealthScore != 100 {
		t.Fatalf("healthy empty-store control: %+v %v", control, err)
	}
	if _, err := s.DB().Exec("DROP TABLE code_node_events"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := e.CheckGuardrail("unknown.go")
		if err == nil || result.Allowed || !strings.Contains(err.Error(), "file health scan") {
			t.Fatalf("broken schema call%d: %+v %v", i, result, err)
		}
	}
	e.LockFileWithOptions("locked.go", "held", "agent", "run", time.Hour)
	locked, err := e.CheckGuardrail("locked.go")
	if err != nil || locked.Allowed || !locked.IsLocked {
		t.Fatalf("lock lost precedence over broken health store: %+v %v", locked, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := e.CheckGuardrail("unknown.go")
	if err == nil || closed.Allowed {
		t.Fatalf("closed-store error became an allow decision: %+v %v", closed, err)
	}
}

func TestCheckGuardrail_NilOptionalStoreStillAllows(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	e := NewEngine(Config{})
	t.Cleanup(e.Close)
	result, err := e.CheckGuardrail("unknown.go")
	if err != nil || !result.Allowed || result.HealthScore != 100 {
		t.Fatalf("optional-store behavior changed: %+v %v", result, err)
	}
}

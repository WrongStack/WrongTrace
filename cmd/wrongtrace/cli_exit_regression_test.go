package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCLIHelperProcess is not a real test: re-executed as a child by the trace
// tests below, it exits with the code in WT_HELPER_EXIT and prints a marker.
func TestCLIHelperProcess(t *testing.T) {
	code := os.Getenv("WT_HELPER_EXIT")
	if code == "" {
		return
	}
	n, _ := strconv.Atoi(code)
	_, _ = os.Stdout.WriteString("child-stdout-marker\n")
	os.Exit(n)
}

func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return strconv.Itoa(port)
}

// `wrongtrace trace` always exited 1 for a failing child, erasing the exit
// code scripts and CI depend on. exitStatus now propagates it verbatim.
func TestExitStatus_PropagatesChildExitCode(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelperProcess$")
	cmd.Env = append(os.Environ(), "WT_HELPER_EXIT=3")
	err := cmd.Run()
	if code, msg := exitStatus(err); code != 3 || msg != "" {
		t.Fatalf("exitStatus(child exit 3) = %d, %q; want 3 and no re-printed message", code, msg)
	}
	if code, msg := exitStatus(errors.New("open db: boom")); code != 1 || msg != "open db: boom" {
		t.Fatalf("exitStatus(plain error) = %d, %q; want 1 with message", code, msg)
	}
}

// Every RunE error dumped the full usage text, and trace banners went to
// stdout where they corrupted piped output of the traced command.
func TestTrace_NoUsageDumpAndBannersOnStderr(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	t.Setenv("WT_HELPER_EXIT", "3")
	if !rootCmd.SilenceUsage || !rootCmd.SilenceErrors {
		t.Fatal("rootCmd must silence cobra's usage dump and its duplicate error print")
	}

	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil) })

	dbPath := filepath.Join(t.TempDir(), "trace.db")
	rootCmd.SetArgs([]string{"trace", "--db", dbPath, "--port", closedPort(t), "--",
		os.Args[0], "-test.run=^TestCLIHelperProcess$"})
	err := rootCmd.Execute()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("trace error = %v, want the child's *exec.ExitError with code 3", err)
	}
	if strings.Contains(stdout.String(), "Usage:") || strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("runtime error dumped usage text:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "[WrongTrace]") {
		t.Errorf("trace banner written to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Profiling command") || !strings.Contains(stderr.String(), "Captured Execution Trace") {
		t.Errorf("trace banners missing from stderr: %q", stderr.String())
	}
}

func TestWaitTimeout_BoundsShutdownWait(t *testing.T) {
	var wg sync.WaitGroup
	release := make(chan struct{})
	wg.Add(1)
	go func() { defer wg.Done(); <-release }()

	start := time.Now()
	if waitTimeout(&wg, 50*time.Millisecond) {
		t.Fatal("waitTimeout reported completion while a goroutine was still running")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("waitTimeout did not return promptly after its deadline")
	}
	close(release)
	if !waitTimeout(&wg, 2*time.Second) {
		t.Fatal("waitTimeout did not observe completion")
	}
}

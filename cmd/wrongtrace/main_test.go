package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRootCmd_HelpAndVersion(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	rootCmd.SetArgs([]string{"--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute --help failed: %v", err)
	}

	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty help output")
	}
}

func TestStatusCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "status_test.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	// Set db flag
	startCmd.Flags().Set("db", dbPath)
	statusCmd.Flags().Set("db", dbPath)

	rootCmd.SetArgs([]string{"status"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute status failed: %v", err)
	}
}

func TestDefaultHelpers(t *testing.T) {
	dataDir := defaultDataDir()
	if len(dataDir) == 0 {
		t.Error("defaultDataDir returned empty string")
	}

	sockPath := defaultSocketPath()
	if len(sockPath) == 0 {
		t.Error("defaultSocketPath returned empty string")
	}

	cwd := mustCwd()
	if len(cwd) == 0 {
		t.Error("mustCwd returned empty string")
	}
}

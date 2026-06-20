package main

import (
	"strings"
	"testing"
)

func TestExtractStatusSuccess(t *testing.T) {
	log := `Starting build...
Compiling sources...
All tests passed
Build completed successfully
exit code 0`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusSuccess {
		t.Errorf("expected status %s, got %s", StatusSuccess, status)
	}
	if matchLine == "" {
		t.Error("expected non-empty match line")
	}
	t.Logf("Success match line: %s", matchLine)
}

func TestExtractStatusFailure(t *testing.T) {
	log := `Starting build...
Compiling sources...
Error: undefined variable
Build failed
exit code 1`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusFailure {
		t.Errorf("expected status %s, got %s", StatusFailure, status)
	}
	if matchLine == "" {
		t.Error("expected non-empty match line")
	}
	t.Logf("Failure match line: %s", matchLine)
}

func TestExtractStatusRunning(t *testing.T) {
	log := `Starting build...
Compiling sources...
Still running tests...
Executing database migration`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusRunning {
		t.Errorf("expected status %s, got %s", StatusRunning, status)
	}
	t.Logf("Running match line: %s", matchLine)
}

func TestExtractStatusUnknown(t *testing.T) {
	log := `Some random text
without any status indicators
just regular lines`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusUnknown {
		t.Errorf("expected status %s, got %s", StatusUnknown, status)
	}
	if matchLine != "" {
		t.Error("expected empty match line for unknown status")
	}
}

func TestExtractStatusSuccessAfterFailure(t *testing.T) {
	log := `Starting build...
First attempt failed with error
Retrying build...
Build completed successfully`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusSuccess {
		t.Errorf("expected status %s (last result should win), got %s", StatusSuccess, status)
	}
	t.Logf("Success (after failure) match line: %s", matchLine)
}

func TestExtractStatusFailureAfterSuccess(t *testing.T) {
	log := `Starting build...
Compilation succeeded
Running tests...
Tests failed`

	lines := strings.Split(log, "\n")
	status, matchLine := extractStatus(lines)

	if status != StatusFailure {
		t.Errorf("expected status %s (last result should win), got %s", StatusFailure, status)
	}
	t.Logf("Failure (after success) match line: %s", matchLine)
}

func TestReadLinesFromFile(t *testing.T) {
	path := "logs/build_success.log"
	lines, err := readLinesFromFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if len(lines) == 0 {
		t.Error("expected non-empty lines")
	}

	status, matchLine := extractStatus(lines)
	if status != StatusSuccess {
		t.Errorf("expected status %s for build_success.log, got %s", StatusSuccess, status)
	}
	t.Logf("build_success.log - Status: %s, Match: %s", status, matchLine)
}

func TestReadLinesFromFileFailure(t *testing.T) {
	path := "logs/build_failure.log"
	lines, err := readLinesFromFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if len(lines) == 0 {
		t.Error("expected non-empty lines")
	}

	status, matchLine := extractStatus(lines)
	if status != StatusFailure {
		t.Errorf("expected status %s for build_failure.log, got %s", StatusFailure, status)
	}
	t.Logf("build_failure.log - Status: %s, Match: %s", status, matchLine)
}

func TestReadLinesFromFileRunning(t *testing.T) {
	path := "logs/build_running.log"
	lines, err := readLinesFromFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if len(lines) == 0 {
		t.Error("expected non-empty lines")
	}

	status, matchLine := extractStatus(lines)
	if status != StatusRunning {
		t.Errorf("expected status %s for build_running.log, got %s", StatusRunning, status)
	}
	t.Logf("build_running.log - Status: %s, Match: %s", status, matchLine)
}

package main

import (
	"fmt"
	"os"
	"runtime"
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

func TestReadTailLinesFromFileSuccess(t *testing.T) {
	path := "logs/build_success.log"
	lines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, DefaultMaxTailLines)
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
	t.Logf("build_success.log - Status: %s, Match: %s, Lines read: %d", status, matchLine, len(lines))
}

func TestReadTailLinesFromFileFailure(t *testing.T) {
	path := "logs/build_failure.log"
	lines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, DefaultMaxTailLines)
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
	t.Logf("build_failure.log - Status: %s, Match: %s, Lines read: %d", status, matchLine, len(lines))
}

func TestReadTailLinesFromFileRunning(t *testing.T) {
	path := "logs/build_running.log"
	lines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, DefaultMaxTailLines)
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
	t.Logf("build_running.log - Status: %s, Match: %s, Lines read: %d", status, matchLine, len(lines))
}

func TestReadTailLinesFromFileLimitedBytes(t *testing.T) {
	path := "logs/build_success.log"

	fullLines, err := readTailLinesFromFile(path, 10*1024*1024, 10000)
	if err != nil {
		t.Fatalf("failed to read full file: %v", err)
	}
	fullCount := len(fullLines)

	limitedLines, err := readTailLinesFromFile(path, 500, 10000)
	if err != nil {
		t.Fatalf("failed to read limited file: %v", err)
	}
	limitedCount := len(limitedLines)

	if limitedCount >= fullCount {
		t.Errorf("expected limited lines (%d) to be fewer than full lines (%d)", limitedCount, fullCount)
	}
	if limitedCount == 0 {
		t.Error("expected some lines to be read even with limited bytes")
	}

	status, _ := extractStatus(limitedLines)
	if status != StatusSuccess {
		t.Errorf("expected status %s even with limited bytes, got %s", StatusSuccess, status)
	}
	t.Logf("Full lines: %d, Limited lines (500 bytes): %d, Status: %s", fullCount, limitedCount, status)
}

func TestReadTailLinesFromFileMaxLines(t *testing.T) {
	path := "logs/build_success.log"

	allLines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, 10000)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	allCount := len(allLines)

	limitedLines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, 5)
	if err != nil {
		t.Fatalf("failed to read file with max lines: %v", err)
	}
	limitedCount := len(limitedLines)

	if limitedCount != 5 {
		t.Errorf("expected 5 lines with max_lines=5, got %d", limitedCount)
	}
	if allCount <= 5 {
		t.Skipf("file only has %d lines, can't test max lines truncation", allCount)
	}

	status, _ := extractStatus(limitedLines)
	if status != StatusSuccess {
		t.Errorf("expected status %s with last 5 lines, got %s", StatusSuccess, status)
	}
	t.Logf("All lines: %d, Last 5 lines: %d", allCount, limitedCount)
}

func generateLargeLogFile(t *testing.T, path string, totalLines int, finalStatus BuildStatus) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create large log file: %v", err)
	}
	defer f.Close()

	for i := 0; i < totalLines; i++ {
		lineType := i % 10
		var line string
		switch lineType {
		case 0:
			line = fmt.Sprintf("[%06d] Compiling module_%d.go...\n", i, i)
		case 1:
			line = fmt.Sprintf("[%06d] Running test case TestCase%d...\n", i, i)
		case 2:
			line = fmt.Sprintf("[%06d] PASS: test TestCase%d passed\n", i, i)
		case 3:
			line = fmt.Sprintf("[%06d] INFO: processing data batch %d\n", i, i)
		case 4:
			line = fmt.Sprintf("[%06d] DEBUG: variable value = %d\n", i, i*10)
		default:
			line = fmt.Sprintf("[%06d] some log content line with various text padding to make it longer\n", i)
		}
		f.WriteString(line)
	}

	switch finalStatus {
	case StatusSuccess:
		f.WriteString("\n")
		f.WriteString("[FINAL] All tests passed successfully\n")
		f.WriteString("[FINAL] Build completed successfully\n")
		f.WriteString("[FINAL] Pipeline finished with exit code 0\n")
	case StatusFailure:
		f.WriteString("\n")
		f.WriteString("[FINAL] Test failures detected\n")
		f.WriteString("[FINAL] Build failed with errors\n")
		f.WriteString("[FINAL] Pipeline finished with exit code 1\n")
	}
}

func TestLargeFileMemoryEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	largeFilePath := "logs/large_build_test.log"
	totalLines := 100000

	t.Logf("Generating large log file with %d lines...", totalLines)
	generateLargeLogFile(t, largeFilePath, totalLines, StatusSuccess)
	defer os.Remove(largeFilePath)

	fileInfo, err := os.Stat(largeFilePath)
	if err != nil {
		t.Fatalf("failed to stat large file: %v", err)
	}
	t.Logf("Large file size: %.2f MB", float64(fileInfo.Size())/1024/1024)

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	lines, err := readTailLinesFromFile(largeFilePath, DefaultMaxTailBytes, DefaultMaxTailLines)
	if err != nil {
		t.Fatalf("failed to read tail of large file: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	allocDiff := m2.Alloc - m1.Alloc
	if allocDiff < 0 {
		allocDiff = 0
	}

	status, matchLine := extractStatus(lines)
	if status != StatusSuccess {
		t.Errorf("expected status %s for large file, got %s", StatusSuccess, status)
	}

	t.Logf("Lines read: %d", len(lines))
	t.Logf("Match line: %s", matchLine)
	t.Logf("Memory allocated for read: %.2f KB", float64(allocDiff)/1024)
	t.Logf("File size: %.2f MB, Heap in use: %.2f MB",
		float64(fileInfo.Size())/1024/1024,
		float64(m2.HeapInuse)/1024/1024)

	maxExpectedBytes := DefaultMaxTailBytes * 3
	if allocDiff > uint64(maxExpectedBytes) {
		t.Errorf("memory usage too high: allocated %d bytes, expected less than %d bytes", allocDiff, maxExpectedBytes)
	}
}

func TestLargeFileFailureStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	largeFilePath := "logs/large_build_failure_test.log"
	totalLines := 50000

	generateLargeLogFile(t, largeFilePath, totalLines, StatusFailure)
	defer os.Remove(largeFilePath)

	lines, err := readTailLinesFromFile(largeFilePath, DefaultMaxTailBytes, DefaultMaxTailLines)
	if err != nil {
		t.Fatalf("failed to read tail of large file: %v", err)
	}

	status, matchLine := extractStatus(lines)
	if status != StatusFailure {
		t.Errorf("expected status %s for large failure file, got %s", StatusFailure, status)
	}

	t.Logf("Large failure file - Lines read: %d, Status: %s, Match: %s", len(lines), status, matchLine)
}

func TestParseIntParam(t *testing.T) {
	tests := []struct {
		input      string
		defaultVal int
		expected   int
	}{
		{"", 100, 100},
		{"500", 100, 500},
		{"0", 100, 100},
		{"-10", 100, 100},
		{"abc", 100, 100},
		{"2000", 500, 2000},
	}

	for _, tt := range tests {
		result := parseIntParam(tt.input, tt.defaultVal)
		if result != tt.expected {
			t.Errorf("parseIntParam(%q, %d) = %d, expected %d", tt.input, tt.defaultVal, result, tt.expected)
		}
	}
}

func TestParseMaxBytesParam(t *testing.T) {
	tests := []struct {
		input      string
		defaultVal int64
		expected   int64
	}{
		{"", 1024, 1024},
		{"2048", 1024, 2048},
		{"0", 1024, 1024},
		{"-100", 1024, 1024},
		{"abc", 1024, 1024},
	}

	for _, tt := range tests {
		result := parseMaxBytesParam(tt.input, tt.defaultVal)
		if result != tt.expected {
			t.Errorf("parseMaxBytesParam(%q, %d) = %d, expected %d", tt.input, tt.defaultVal, result, tt.expected)
		}
	}
}

func TestEmptyFile(t *testing.T) {
	path := "logs/empty.log"
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create empty file: %v", err)
	}
	f.Close()
	defer os.Remove(path)

	lines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, DefaultMaxTailLines)
	if err != nil {
		t.Fatalf("failed to read empty file: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty file, got %d", len(lines))
	}

	status, _ := extractStatus(lines)
	if status != StatusUnknown {
		t.Errorf("expected unknown status for empty file, got %s", status)
	}
}

func TestSingleLineFile(t *testing.T) {
	path := "logs/single_line.log"
	content := "Build completed successfully"
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to create single line file: %v", err)
	}
	defer os.Remove(path)

	lines, err := readTailLinesFromFile(path, DefaultMaxTailBytes, DefaultMaxTailLines)
	if err != nil {
		t.Fatalf("failed to read single line file: %v", err)
	}
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != content {
		t.Errorf("expected content %q, got %q", content, lines[0])
	}
}

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type BuildStatus string

const (
	StatusSuccess BuildStatus = "success"
	StatusFailure BuildStatus = "failure"
	StatusUnknown BuildStatus = "unknown"
	StatusRunning BuildStatus = "running"
)

type LogSource string

const (
	SourceFile LogSource = "file"
	SourceURL  LogSource = "url"
)

type StatusResponse struct {
	Status   BuildStatus `json:"status"`
	Message  string      `json:"message"`
	Source   LogSource   `json:"source"`
	Location string      `json:"location"`
	MatchLine string     `json:"match_line,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type ErrorCategory string

const (
	ErrorCategoryCompile     ErrorCategory = "compile"
	ErrorCategoryTest        ErrorCategory = "test"
	ErrorCategoryDeploy      ErrorCategory = "deploy"
	ErrorCategoryDependency  ErrorCategory = "dependency"
	ErrorCategoryRuntime     ErrorCategory = "runtime"
	ErrorCategoryConfig      ErrorCategory = "config"
	ErrorCategoryNetwork     ErrorCategory = "network"
	ErrorCategoryPermission  ErrorCategory = "permission"
	ErrorCategoryOther       ErrorCategory = "other"
)

type ErrorEntry struct {
	Line     int          `json:"line"`
	Content  string       `json:"content"`
	Category ErrorCategory `json:"category"`
}

type ErrorsResponse struct {
	Total   int           `json:"total"`
	Source  LogSource     `json:"source"`
	Location string       `json:"location"`
	Errors  []ErrorEntry  `json:"errors"`
	Stats   map[ErrorCategory]int `json:"stats"`
}

const (
	DefaultMaxTailBytes = 1 * 1024 * 1024
	DefaultMaxTailLines = 2000
	DefaultMaxErrors    = 100
	readBufferSize      = 64 * 1024
)

var successPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(build\s+success(ful)?|pipeline\s+success|job\s+success|deploy\s+success)\b`),
	regexp.MustCompile(`(?i)\b(success|succeeded|passed)\b`),
	regexp.MustCompile(`^\s*✅|^\s*\[SUCCESS\]|^\s*BUILD SUCCESS`),
	regexp.MustCompile(`(?i)\bexit\s+code\s+0\b`),
	regexp.MustCompile(`(?i)\bbuild\s+completed\s+successfully\b`),
	regexp.MustCompile(`(?i)\b(all\s+tests\s+passed|tests\s+passed)\b`),
	regexp.MustCompile(`^\s*##\[section\].*success`),
}

var failurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(build\s+(failure|fail(ed)?|error))\b`),
	regexp.MustCompile(`(?i)\b(pipeline\s+(failure|fail(ed)?)|job\s+(failure|fail(ed)?))\b`),
	regexp.MustCompile(`(?i)\b(deploy\s+(failure|fail(ed)?))\b`),
	regexp.MustCompile(`(?i)\b(failure|failed|fatal)\b`),
	regexp.MustCompile(`^\s*❌|^\s*\[FAILURE\]|^\s*BUILD FAILED|^\s*BUILD FAILURE`),
	regexp.MustCompile(`(?i)\bexit\s+code\s+[1-9]\d*\b`),
	regexp.MustCompile(`(?i)\b(error(s)?|exception|panic)\b`),
	regexp.MustCompile(`(?i)\b(test(s)?\s+(fail(ed)?|failure))\b`),
}

var runningPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(build\s+in\s+progress|running|executing)\b`),
	regexp.MustCompile(`(?i)\b(still\s+running|pending|queued)\b`),
}

var compileErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(syntax error|type error|undefined|cannot find|compilation error|compile error)\b`),
	regexp.MustCompile(`(?i)(SyntaxError|TypeError|ReferenceError|NameError)`),
	regexp.MustCompile(`(?i)\b(\.go:\d+:\d+:|\.ts\(\d+,\d+\): error|\.js:\d+|\.py:\d+:\s*NameError|\.py:\d+:\s*SyntaxError)\b`),
	regexp.MustCompile(`(?i)\b(undefined reference|multiple definition|linker error|link error)\b`),
	regexp.MustCompile(`(?i)\b(compilation failed|compile failed)\b`),
	regexp.MustCompile(`(?i)\b(unresolved symbol|no such file or directory.*include|no such file or directory.*header)\b`),
	regexp.MustCompile(`(?i)\b(error:.*\.(go|ts|js|py|java|cpp|c|rs|cs))\b`),
}

var testErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(test\s+(fail(ed)?|failure)|tests\s+(fail(ed)?|failure))\b`),
	regexp.MustCompile(`(?i)\b(FAIL|assertion failed|assert failed)\b`),
	regexp.MustCompile(`(?i)\b(expect(ed)? .* but got|expected: .* actual:)\b`),
	regexp.MustCompile(`(?i)\b(unit test.*fail|integration test.*fail|e2e.*fail)\b`),
	regexp.MustCompile(`(?i)\b(test case.*fail|test suite.*fail)\b`),
}

var deployErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(deploy\s+(fail(ed)?|error|failure|abort))\b`),
	regexp.MustCompile(`(?i)\b(deployment\s+(fail(ed)?|error|failure|abort))\b`),
	regexp.MustCompile(`(?i)\b(rollback|deploy rollback|deployment rollback)\b`),
	regexp.MustCompile(`(?i)\b(health check.*fail|readiness probe.*fail|liveness probe.*fail)\b`),
	regexp.MustCompile(`(?i)\b(pod\s+(pending|error|crashloopbackoff)|container.*fail)\b`),
	regexp.MustCompile(`(?i)\b(canary.*fail|blue.?green.*fail)\b`),
	regexp.MustCompile(`(?i)\b(fail(ed)?.*deploy|error.*deploy)\b`),
	regexp.MustCompile(`(?i)\b(deploy.*denied|deployment.*denied)\b`),
}

var dependencyErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(dependency.*error|dependency.*fail)\b`),
	regexp.MustCompile(`(?i)\b(could not resolve dependency|unresolved dependency)\b`),
	regexp.MustCompile(`(?i)\b(version conflict|dependency conflict|version mismatch)\b`),
	regexp.MustCompile(`(?i)\b(no matching version|module not found|package not found)\b`),
	regexp.MustCompile(`(?i)\b(go mod.*error|npm install.*fail|pip install.*fail|yarn.*fail)\b`),
	regexp.MustCompile(`(?i)\b(network error while fetching|fetch error.*package|registry error)\b`),
}

var runtimeErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(null pointer|nil pointer|segmentation fault|segfault)\b`),
	regexp.MustCompile(`(?i)\b(panic|fatal error|crash)\b`),
	regexp.MustCompile(`(?i)\b(out of memory|OOM|heap overflow|stack overflow)\b`),
	regexp.MustCompile(`(?i)\b(deadlock|timeout.*exceeded|context deadline exceeded)\b`),
	regexp.MustCompile(`(?i)\b(exception|throw.*error|uncaught)\b`),
}

var configErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(config.*error|config.*fail|configuration.*error|configuration.*fail)\b`),
	regexp.MustCompile(`(?i)\b(invalid config|invalid.*config|malformed config|bad config)\b`),
	regexp.MustCompile(`(?i)\b(missing config|required config|config not found)\b`),
	regexp.MustCompile(`(?i)\b(yaml error|yaml.*error|json error|json.*error|toml error|parse error.*config)\b`),
	regexp.MustCompile(`(?i)\b(environment variable.*miss|env var.*required|missing.*environment)\b`),
	regexp.MustCompile(`(?i)\b(config validation.*fail|configuration validation.*fail)\b`),
	regexp.MustCompile(`(?i)\b(error.*config|fail.*config)\b`),
}

var networkErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(connection refused|connection reset|connection timeout)\b`),
	regexp.MustCompile(`(?i)\b(dns error|DNS resolution|no such host)\b`),
	regexp.MustCompile(`(?i)\b(network error|network failure|network timeout)\b`),
	regexp.MustCompile(`(?i)\b(ssl error|TLS error|certificate.*error)\b`),
	regexp.MustCompile(`(?i)\b(request timeout|gateway timeout|504|502)\b`),
}

var permissionErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(permission denied|access denied|forbidden)\b`),
	regexp.MustCompile(`(?i)\b(unauthorized|authentication failed|auth fail)\b`),
	regexp.MustCompile(`(?i)\b(access token.*invalid|invalid credentials|bad credentials)\b`),
	regexp.MustCompile(`(?i)\b(EACCES|EPERM)\b`),
}

type errorCategoryMatcher struct {
	category ErrorCategory
	patterns []*regexp.Regexp
}

var errorMatchers = []errorCategoryMatcher{
	{ErrorCategoryTest, testErrorPatterns},
	{ErrorCategoryDeploy, deployErrorPatterns},
	{ErrorCategoryPermission, permissionErrorPatterns},
	{ErrorCategoryConfig, configErrorPatterns},
	{ErrorCategoryNetwork, networkErrorPatterns},
	{ErrorCategoryDependency, dependencyErrorPatterns},
	{ErrorCategoryRuntime, runtimeErrorPatterns},
	{ErrorCategoryCompile, compileErrorPatterns},
}

func extractStatus(lines []string) (BuildStatus, string) {
	lastSuccess := -1
	lastFailure := -1
	lastRunning := -1
	var successLine, failureLine, runningLine string

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]

		if lastFailure == -1 {
			for _, pattern := range failurePatterns {
				if pattern.MatchString(line) {
					lastFailure = i
					failureLine = strings.TrimSpace(line)
					break
				}
			}
		}

		if lastSuccess == -1 {
			for _, pattern := range successPatterns {
				if pattern.MatchString(line) {
					lastSuccess = i
					successLine = strings.TrimSpace(line)
					break
				}
			}
		}

		if lastRunning == -1 {
			for _, pattern := range runningPatterns {
				if pattern.MatchString(line) {
					lastRunning = i
					runningLine = strings.TrimSpace(line)
					break
				}
			}
		}

		if lastSuccess != -1 && lastFailure != -1 {
			break
		}
	}

	switch {
	case lastSuccess == -1 && lastFailure == -1 && lastRunning == -1:
		return StatusUnknown, ""
	case lastSuccess != -1 && lastFailure == -1:
		return StatusSuccess, successLine
	case lastFailure != -1 && lastSuccess == -1:
		return StatusFailure, failureLine
	case lastSuccess > lastFailure:
		return StatusSuccess, successLine
	case lastFailure > lastSuccess:
		return StatusFailure, failureLine
	default:
		if lastRunning > lastSuccess && lastRunning > lastFailure {
			return StatusRunning, runningLine
		}
		return StatusUnknown, ""
	}
}

func classifyError(line string) (ErrorCategory, bool) {
	for _, matcher := range errorMatchers {
		for _, pattern := range matcher.patterns {
			if pattern.MatchString(line) {
				return matcher.category, true
			}
		}
	}
	return ErrorCategoryOther, false
}

func extractErrors(lines []string, maxErrors int, categories []ErrorCategory) ([]ErrorEntry, map[ErrorCategory]int) {
	if maxErrors <= 0 {
		maxErrors = DefaultMaxErrors
	}

	categorySet := make(map[ErrorCategory]bool)
	for _, c := range categories {
		categorySet[c] = true
	}
	filterByCategory := len(categorySet) > 0

	var errors []ErrorEntry
	stats := make(map[ErrorCategory]int)

	for i, line := range lines {
		category, matched := classifyError(line)
		if !matched {
			continue
		}

		if filterByCategory && !categorySet[category] {
			continue
		}

		if len(errors) >= maxErrors {
			stats[category]++
			continue
		}

		errors = append(errors, ErrorEntry{
			Line:     i + 1,
			Content:  strings.TrimSpace(line),
			Category: category,
		})
		stats[category]++
	}

	return errors, stats
}

func readTailLinesFromFile(path string, maxBytes int64, maxLines int) ([]string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	fileSize := fileInfo.Size()
	if fileSize == 0 {
		return []string{}, nil
	}

	readSize := maxBytes
	if readSize > fileSize {
		readSize = fileSize
	}

	startPos := fileSize - readSize

	buf := make([]byte, readSize)
	_, err = f.ReadAt(buf, startPos)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read file tail: %w", err)
	}

	if startPos > 0 {
		firstNewline := -1
		for i := 0; i < len(buf); i++ {
			if buf[i] == '\n' {
				firstNewline = i
				break
			}
		}
		if firstNewline >= 0 {
			buf = buf[firstNewline+1:]
		}
	}

	content := string(buf)
	rawLines := strings.Split(content, "\n")
	var lines []string
	for _, line := range rawLines {
		lines = append(lines, strings.TrimRight(line, "\r"))
	}

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return lines, nil
}

func readTailLinesFromURL(rawURL string, maxLines int) ([]string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme: %s", parsedURL.Scheme)
	}

	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	ring := make([]string, maxLines)
	writeIdx := 0
	count := 0

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		ring[writeIdx] = strings.TrimRight(scanner.Text(), "\r")
		writeIdx = (writeIdx + 1) % maxLines
		if count < maxLines {
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to stream response body: %w", err)
	}

	result := make([]string, count)
	if count < maxLines {
		copy(result, ring[:count])
	} else {
		copy(result, ring[writeIdx:])
		copy(result[maxLines-writeIdx:], ring[:writeIdx])
	}

	return result, nil
}

func parseIntParam(s string, defaultValue int) int {
	if s == "" {
		return defaultValue
	}
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil || v <= 0 {
		return defaultValue
	}
	return v
}

func parseMaxBytesParam(s string, defaultValue int64) int64 {
	if s == "" {
		return defaultValue
	}
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil || v <= 0 {
		return defaultValue
	}
	return v
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

type fileStatusRequest struct {
	Path     string `json:"path"`
	MaxBytes string `json:"max_bytes,omitempty"`
	MaxLines string `json:"max_lines,omitempty"`
}

func handleStatusFromFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var path string
	var maxBytesStr string
	var maxLinesStr string

	if r.Method == http.MethodGet {
		path = r.URL.Query().Get("path")
		maxBytesStr = r.URL.Query().Get("max_bytes")
		maxLinesStr = r.URL.Query().Get("max_lines")
	} else {
		var req fileStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		path = req.Path
		maxBytesStr = req.MaxBytes
		maxLinesStr = req.MaxLines
	}

	if path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path parameter is required"})
		return
	}

	maxBytes := parseMaxBytesParam(maxBytesStr, DefaultMaxTailBytes)
	maxLines := parseIntParam(maxLinesStr, DefaultMaxTailLines)

	lines, err := readTailLinesFromFile(path, maxBytes, maxLines)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	status, matchLine := extractStatus(lines)

	message := "Build status extracted successfully (tail read)"
	if status == StatusUnknown {
		message = "Could not determine build status from log (tail read)"
	} else if status == StatusRunning {
		message = "Build appears to be still running (tail read)"
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:    status,
		Message:   message,
		Source:    SourceFile,
		Location:  path,
		MatchLine: matchLine,
	})
}

type urlStatusRequest struct {
	URL      string `json:"url"`
	MaxLines string `json:"max_lines,omitempty"`
}

func handleStatusFromURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var rawURL string
	var maxLinesStr string

	if r.Method == http.MethodGet {
		rawURL = r.URL.Query().Get("url")
		maxLinesStr = r.URL.Query().Get("max_lines")
	} else {
		var req urlStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		rawURL = req.URL
		maxLinesStr = req.MaxLines
	}

	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "url parameter is required"})
		return
	}

	maxLines := parseIntParam(maxLinesStr, DefaultMaxTailLines)

	lines, err := readTailLinesFromURL(rawURL, maxLines)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	status, matchLine := extractStatus(lines)

	message := "Build status extracted successfully (stream tail read)"
	if status == StatusUnknown {
		message = "Could not determine build status from log (stream tail read)"
	} else if status == StatusRunning {
		message = "Build appears to be still running (stream tail read)"
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:    status,
		Message:   message,
		Source:    SourceURL,
		Location:  rawURL,
		MatchLine: matchLine,
	})
}

func handleStatusFromContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "content field is required"})
		return
	}

	lines := strings.Split(req.Content, "\n")
	status, matchLine := extractStatus(lines)

	message := "Build status extracted successfully"
	if status == StatusUnknown {
		message = "Could not determine build status from log"
	} else if status == StatusRunning {
		message = "Build appears to be still running"
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:    status,
		Message:   message,
		Source:    "content",
		Location:  "inline",
		MatchLine: matchLine,
	})
}

func parseCategoriesParam(csv string) []ErrorCategory {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	var result []ErrorCategory
	for _, p := range parts {
		cat := ErrorCategory(strings.TrimSpace(strings.ToLower(p)))
		switch cat {
		case ErrorCategoryCompile, ErrorCategoryTest, ErrorCategoryDeploy,
			ErrorCategoryDependency, ErrorCategoryRuntime, ErrorCategoryConfig,
			ErrorCategoryNetwork, ErrorCategoryPermission, ErrorCategoryOther:
			result = append(result, cat)
		}
	}
	return result
}

type fileErrorsRequest struct {
	Path       string `json:"path"`
	MaxBytes   string `json:"max_bytes,omitempty"`
	MaxLines   string `json:"max_lines,omitempty"`
	MaxErrors  string `json:"max_errors,omitempty"`
	Categories string `json:"categories,omitempty"`
}

func handleErrorsFromFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var path string
	var maxBytesStr string
	var maxLinesStr string
	var maxErrorsStr string
	var categoriesStr string

	if r.Method == http.MethodGet {
		path = r.URL.Query().Get("path")
		maxBytesStr = r.URL.Query().Get("max_bytes")
		maxLinesStr = r.URL.Query().Get("max_lines")
		maxErrorsStr = r.URL.Query().Get("max_errors")
		categoriesStr = r.URL.Query().Get("categories")
	} else {
		var req fileErrorsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		path = req.Path
		maxBytesStr = req.MaxBytes
		maxLinesStr = req.MaxLines
		maxErrorsStr = req.MaxErrors
		categoriesStr = req.Categories
	}

	if path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path parameter is required"})
		return
	}

	maxBytes := parseMaxBytesParam(maxBytesStr, DefaultMaxTailBytes)
	maxLines := parseIntParam(maxLinesStr, DefaultMaxTailLines)
	maxErrors := parseIntParam(maxErrorsStr, DefaultMaxErrors)
	categories := parseCategoriesParam(categoriesStr)

	lines, err := readTailLinesFromFile(path, maxBytes, maxLines)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	errors, stats := extractErrors(lines, maxErrors, categories)

	writeJSON(w, http.StatusOK, ErrorsResponse{
		Total:    len(errors),
		Source:   SourceFile,
		Location: path,
		Errors:   errors,
		Stats:    stats,
	})
}

type urlErrorsRequest struct {
	URL        string `json:"url"`
	MaxLines   string `json:"max_lines,omitempty"`
	MaxErrors  string `json:"max_errors,omitempty"`
	Categories string `json:"categories,omitempty"`
}

func handleErrorsFromURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var rawURL string
	var maxLinesStr string
	var maxErrorsStr string
	var categoriesStr string

	if r.Method == http.MethodGet {
		rawURL = r.URL.Query().Get("url")
		maxLinesStr = r.URL.Query().Get("max_lines")
		maxErrorsStr = r.URL.Query().Get("max_errors")
		categoriesStr = r.URL.Query().Get("categories")
	} else {
		var req urlErrorsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		rawURL = req.URL
		maxLinesStr = req.MaxLines
		maxErrorsStr = req.MaxErrors
		categoriesStr = req.Categories
	}

	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "url parameter is required"})
		return
	}

	maxLines := parseIntParam(maxLinesStr, DefaultMaxTailLines)
	maxErrors := parseIntParam(maxErrorsStr, DefaultMaxErrors)
	categories := parseCategoriesParam(categoriesStr)

	lines, err := readTailLinesFromURL(rawURL, maxLines)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	errors, stats := extractErrors(lines, maxErrors, categories)

	writeJSON(w, http.StatusOK, ErrorsResponse{
		Total:    len(errors),
		Source:   SourceURL,
		Location: rawURL,
		Errors:   errors,
		Stats:    stats,
	})
}

type contentErrorsRequest struct {
	Content    string `json:"content"`
	MaxErrors  string `json:"max_errors,omitempty"`
	Categories string `json:"categories,omitempty"`
}

func handleErrorsFromContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var req contentErrorsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "content field is required"})
		return
	}

	maxErrors := parseIntParam(req.MaxErrors, DefaultMaxErrors)
	categories := parseCategoriesParam(req.Categories)

	lines := strings.Split(req.Content, "\n")
	errors, stats := extractErrors(lines, maxErrors, categories)

	writeJSON(w, http.StatusOK, ErrorsResponse{
		Total:    len(errors),
		Source:   "content",
		Location: "inline",
		Errors:   errors,
		Stats:    stats,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"service": "ci-log-reader",
	})
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/status/file", handleStatusFromFile)
	mux.HandleFunc("/api/status/url", handleStatusFromURL)
	mux.HandleFunc("/api/status/content", handleStatusFromContent)
	mux.HandleFunc("/api/errors/file", handleErrorsFromFile)
	mux.HandleFunc("/api/errors/url", handleErrorsFromURL)
	mux.HandleFunc("/api/errors/content", handleErrorsFromContent)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	fmt.Printf("CI Log Reader server starting on %s\n", addr)
	fmt.Printf("Status endpoints:\n")
	fmt.Printf("  GET/POST /api/status/file?path=<file_path>\n")
	fmt.Printf("  GET/POST /api/status/url?url=<log_url>\n")
	fmt.Printf("  POST /api/status/content (body: {\"content\": \"<log_content>\"})\n")
	fmt.Printf("Error search endpoints:\n")
	fmt.Printf("  GET/POST /api/errors/file?path=<file_path>&categories=compile,test&max_errors=50\n")
	fmt.Printf("  GET/POST /api/errors/url?url=<log_url>&categories=deploy\n")
	fmt.Printf("  POST /api/errors/content (body: {\"content\": \"...\", \"categories\": \"compile,runtime\"})\n")
	fmt.Printf("  GET /health\n")
	fmt.Printf("Categories: compile, test, deploy, dependency, runtime, config, network, permission\n")

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

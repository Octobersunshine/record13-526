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

func readLinesFromFile(path string) ([]string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return lines, nil
}

func readLinesFromURL(rawURL string) ([]string, error) {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	lines := strings.Split(string(body), "\n")
	return lines, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func handleStatusFromFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var path string
	if r.Method == http.MethodGet {
		path = r.URL.Query().Get("path")
	} else {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		path = req.Path
	}

	if path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path parameter is required"})
		return
	}

	lines, err := readLinesFromFile(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

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
		Source:    SourceFile,
		Location:  path,
		MatchLine: matchLine,
	})
}

func handleStatusFromURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
		return
	}

	var rawURL string
	if r.Method == http.MethodGet {
		rawURL = r.URL.Query().Get("url")
	} else {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
			return
		}
		rawURL = req.URL
	}

	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "url parameter is required"})
		return
	}

	lines, err := readLinesFromURL(rawURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	fmt.Printf("CI Log Reader server starting on %s\n", addr)
	fmt.Printf("Endpoints:\n")
	fmt.Printf("  GET/POST /api/status/file?path=<file_path>\n")
	fmt.Printf("  GET/POST /api/status/url?url=<log_url>\n")
	fmt.Printf("  POST /api/status/content (body: {\"content\": \"<log_content>\"})\n")
	fmt.Printf("  GET /health\n")

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

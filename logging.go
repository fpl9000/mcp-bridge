// logging.go implements structured JSON-line logging to a file, per design spec
// Section 3.22. The bridge speaks MCP/JSON-RPC over stdout, so logging must
// never touch stdout; this logger writes to the configured log file (and
// optionally mirrors to stderr, which is safe because the stdio transport
// only uses stdout).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Log levels, ordered from least to most severe. A configured level filters
// out any event below it.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// levelRank maps a level name to a numeric severity so Logger can compare the
// configured threshold against an incoming event's level.
var levelRank = map[string]int{
	LogLevelDebug: 0,
	LogLevelInfo:  1,
	LogLevelWarn:  2,
	LogLevelError: 3,
}

// Logger writes structured JSON-line log entries to a file. Writes are
// serialized by a mutex because multiple tool-handler goroutines may log
// concurrently (the mcp-go stdio server uses a worker pool).
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	minLevel int
}

// logLine is the JSON shape of one log entry, matching the format shown in
// design spec Section 3.22 (e.g. {"ts":...,"level":"warn","msg":"...",...}).
type logLine struct {
	Timestamp string         `json:"ts"`
	Level     string         `json:"level"`
	Message   string         `json:"msg"`
	Fields    map[string]any `json:"-"`
}

// MarshalJSON flattens Fields alongside the fixed ts/level/msg keys, so
// callers can attach arbitrary structured context without a nested object.
func (l logLine) MarshalJSON() ([]byte, error) {
	merged := map[string]any{
		"ts":    l.Timestamp,
		"level": l.Level,
		"msg":   l.Message,
	}

	for key, value := range l.Fields {
		merged[key] = value
	}

	return json.Marshal(merged)
}

// NewLogger opens (creating if needed) the log file at path and returns a
// Logger that filters events below minLevel. The caller is responsible for
// calling Close when the bridge shuts down.
func NewLogger(path string, minLevel string) (*Logger, error) {
	// Ensure the log file's parent directory exists; config validation
	// already checks this, but a fresh directory layout may not have it yet
	// (e.g. the memory directory was just created).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	rank, ok := levelRank[minLevel]
	if !ok {
		// An unrecognized configured level defaults to "info" rather than
		// failing bridge startup over a cosmetic config typo.
		rank = levelRank[LogLevelInfo]
	}

	return &Logger{file: file, minLevel: rank}, nil
}

// log writes one entry if its level meets the configured threshold.
func (l *Logger) log(level string, msg string, fields map[string]any) {
	if levelRank[level] < l.minLevel {
		return
	}

	entry := logLine{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Level:     level,
		Message:   msg,
		Fields:    fields,
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		// Marshaling a map[string]any of caller-supplied values could
		// theoretically fail (e.g. a channel value); drop the entry rather
		// than crash the bridge over a logging failure.
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.file, string(encoded))
}

func (l *Logger) Debug(msg string, fields map[string]any) { l.log(LogLevelDebug, msg, fields) }
func (l *Logger) Info(msg string, fields map[string]any)  { l.log(LogLevelInfo, msg, fields) }
func (l *Logger) Warn(msg string, fields map[string]any)  { l.log(LogLevelWarn, msg, fields) }
func (l *Logger) Error(msg string, fields map[string]any) { l.log(LogLevelError, msg, fields) }

// Close flushes and closes the underlying log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

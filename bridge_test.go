// bridge_test.go holds shared test scaffolding used by every _test.go file in
// this package: a fresh Bridge wired to a temporary memory directory (one per
// test, per Chapter 8's testing strategy), and small helpers for invoking a
// tool handler directly and decoding its JSON result.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// newTestBridge builds a Bridge against a fresh temporary memory directory.
func newTestBridge(t *testing.T) *Bridge {
	t.Helper()

	dir := t.TempDir()

	cfg := &Config{
		Memory: MemoryConfig{
			Directory:        dir,
			SummaryMaxLength: 200,
		},
		Handle: HandleConfig{
			IDLength:      8,
			RetentionDays: 60,
		},
		Persistence: PersistenceConfig{
			StateFile:                 ".bridge-state.json",
			CheckpointIntervalSeconds: 1,
		},
		Logging: LoggingConfig{
			File:  filepath.Join(dir, "bridge.log"),
			Level: "debug",
		},
	}

	if err := cfg.validate(); err != nil {
		t.Fatalf("config validation failed: %v", err)
	}

	logger, err := NewLogger(cfg.Logging.File, cfg.Logging.Level)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	t.Cleanup(func() { logger.Close() })

	handleMap := NewHandleMap(cfg.Handle.IDLength)
	persist, _ := NewPersistence(cfg, handleMap, logger)

	return &Bridge{
		Config:     cfg,
		Logger:     logger,
		Handles:    handleMap,
		Mutex:      &MemMutex{},
		Persist:    persist,
		IndexCache: NewIndexCache(),
	}
}

// toolHandler is the fixed signature every registered MCP tool handler uses.
type toolHandler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// callTool invokes handler with the given arguments and returns the raw
// CallToolResult, for tests that need to inspect IsError directly.
func callTool(t *testing.T, handler toolHandler, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	request := mcp.CallToolRequest{}
	request.Params.Arguments = args

	result, err := handler(context.Background(), request)
	if err != nil {
		t.Fatalf("handler returned unexpected Go error: %v", err)
	}

	return result
}

// resultText extracts the text of a CallToolResult's first content item.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatalf("tool result has no content")
	}

	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool result content is not TextContent: %#v", result.Content[0])
	}

	return text.Text
}

// callToolJSON invokes handler and decodes the result text as a JSON object,
// for tests that just want to assert on response fields.
func callToolJSON(t *testing.T, handler toolHandler, args map[string]any) map[string]any {
	t.Helper()

	result := callTool(t, handler, args)

	var decoded map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &decoded); err != nil {
		t.Fatalf("decoding tool result as JSON: %v (text: %q)", err, resultText(t, result))
	}

	return decoded
}

// startConversation is a convenience wrapper used by nearly every test: mint
// a handle via memory_start_conversation and return it.
func startConversation(t *testing.T, b *Bridge) string {
	t.Helper()

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)
	handle, ok := response["handle"].(string)
	if !ok || handle == "" {
		t.Fatalf("memory_start_conversation did not return a handle: %#v", response)
	}

	return handle
}

// assertErrorCode asserts that a decoded tool response is an error envelope
// (Section 3.19) carrying the expected stable error code.
func assertErrorCode(t *testing.T, response map[string]any, wantCode string) {
	t.Helper()

	if ok, present := response["ok"].(bool); !present || ok {
		t.Fatalf("expected an error response (ok: false), got %#v", response)
	}

	errorField, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error object in response, got %#v", response)
	}

	gotCode, _ := errorField["code"].(string)
	if gotCode != wantCode {
		t.Fatalf("expected error code %q, got %q (full response: %#v)", wantCode, gotCode, response)
	}
}

// readFileString reads a file's full content as a string, failing the test
// on any error.
func readFileString(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}

	return string(raw)
}

// fileExists reports whether path exists on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

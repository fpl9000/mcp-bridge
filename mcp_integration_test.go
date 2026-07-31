// mcp_integration_test.go covers design spec Section 8.1.15: the bridge
// works correctly as an MCP server. These tests drive the real mcp-go
// server through JSON-RPC messages via HandleMessage — the same processing
// path ServeStdio uses for each line of stdin — rather than calling tool
// handlers directly, so the MCP layer itself (registration, schema
// advertisement, dispatch) is under test, not just the handlers.
package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// newTestMCPServer builds a real MCP server with all eight memory tools
// registered against b, mirroring what main.go does at startup.
func newTestMCPServer(b *Bridge) *server.MCPServer {
	s := server.NewMCPServer(
		"mcp-bridge",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	RegisterTools(s, b)
	return s
}

// jsonRPCRequest marshals a JSON-RPC 2.0 request envelope for HandleMessage.
func jsonRPCRequest(t *testing.T, id int, method string, params any) json.RawMessage {
	t.Helper()

	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		envelope["params"] = params
	}

	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshaling JSON-RPC request: %v", err)
	}

	return raw
}

// TestMCP_InitializeHandshake covers "MCP initialization handshake": the
// bridge responds with its capabilities, including tool support.
func TestMCP_InitializeHandshake(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)

	request := jsonRPCRequest(t, 1, string(mcp.MethodInitialize), mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test-client", Version: "1.0.0"},
	})

	response := s.HandleMessage(context.Background(), request)

	rpcResponse, ok := response.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected a JSON-RPC response, got %#v", response)
	}

	resultBytes, err := json.Marshal(rpcResponse.Result)
	if err != nil {
		t.Fatalf("marshaling initialize result: %v", err)
	}

	var initResult mcp.InitializeResult
	if err := json.Unmarshal(resultBytes, &initResult); err != nil {
		t.Fatalf("decoding initialize result: %v", err)
	}

	if initResult.Capabilities.Tools == nil {
		t.Fatalf("expected tool capabilities to be advertised, got %#v", initResult.Capabilities)
	}
}

// TestMCP_ToolListing covers "Tool listing": the server returns exactly the
// eight in-scope memory tools (Section 3 of IMPLEMENTATION-PROMPT-minimal.md — the
// full twelve-tool design's spawn_agent, check_agent, run_command, and
// memory_run_maintenance are out of scope for this build).
func TestMCP_ToolListing(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)

	request := jsonRPCRequest(t, 1, string(mcp.MethodToolsList), map[string]any{})
	response := s.HandleMessage(context.Background(), request)

	rpcResponse, ok := response.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected a JSON-RPC response, got %#v", response)
	}

	resultBytes, err := json.Marshal(rpcResponse.Result)
	if err != nil {
		t.Fatalf("marshaling tools/list result: %v", err)
	}

	var listResult mcp.ListToolsResult
	if err := json.Unmarshal(resultBytes, &listResult); err != nil {
		t.Fatalf("decoding tools/list result: %v", err)
	}

	want := map[string]bool{
		"memory_start_conversation": false,
		"memory_get_core":           false,
		"memory_write_core":         false,
		"memory_get_index":          false,
		"memory_get_block":          false,
		"memory_write_block":        false,
		"memory_append_block":       false,
		"memory_append_episodic":    false,
	}

	for _, tool := range listResult.Tools {
		if _, expected := want[tool.Name]; !expected {
			t.Fatalf("unexpected tool registered: %q (out of scope for this build)", tool.Name)
		}
		want[tool.Name] = true
	}

	for name, seen := range want {
		if !seen {
			t.Fatalf("expected tool %q to be registered, but it was not", name)
		}
	}
}

// TestMCP_ToolCallWithValidParams covers "Tool call with valid params":
// invoking memory_start_conversation via tools/call returns a well-formed
// result.
func TestMCP_ToolCallWithValidParams(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)

	request := jsonRPCRequest(t, 1, string(mcp.MethodToolsCall), mcp.CallToolParams{
		Name: "memory_start_conversation",
	})

	response := s.HandleMessage(context.Background(), request)

	rpcResponse, ok := response.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected a JSON-RPC response, got %#v", response)
	}

	resultBytes, err := json.Marshal(rpcResponse.Result)
	if err != nil {
		t.Fatalf("marshaling tools/call result: %v", err)
	}

	var callResult mcp.CallToolResult
	if err := json.Unmarshal(resultBytes, &callResult); err != nil {
		t.Fatalf("decoding tools/call result: %v", err)
	}

	if callResult.IsError {
		t.Fatalf("expected a successful tool result, got an error result: %#v", callResult)
	}
}

// TestMCP_ToolCallMissingRequiredParam covers "Tool call with missing
// required param": omitting memory_write_core's required content parameter
// returns an MCP-level error result with a descriptive message.
func TestMCP_ToolCallMissingRequiredParam(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)
	handle := startConversation(t, b)

	request := jsonRPCRequest(t, 1, string(mcp.MethodToolsCall), mcp.CallToolParams{
		Name:      "memory_write_core",
		Arguments: map[string]any{"handle": handle},
	})

	response := s.HandleMessage(context.Background(), request)
	assertToolCallIsError(t, response)
}

// TestMCP_MissingHandleParam covers "Missing handle param on memory tool":
// handle is required on every memory tool except memory_start_conversation,
// and omitting it returns an MCP error.
func TestMCP_MissingHandleParam(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)

	request := jsonRPCRequest(t, 1, string(mcp.MethodToolsCall), mcp.CallToolParams{
		Name:      "memory_get_core",
		Arguments: map[string]any{},
	})

	response := s.HandleMessage(context.Background(), request)
	assertToolCallIsError(t, response)
}

// assertToolCallIsError decodes a tools/call HandleMessage response and
// fails the test unless it represents an error — either a JSON-RPC
// protocol-level error or a CallToolResult with IsError set (Section 12.7's
// two-tier error model: mcp-go does not itself validate required
// parameters, so the bridge's own handlers report these as tool-level
// errors rather than protocol-level ones, but either shape satisfies "the
// bridge did not silently succeed").
func assertToolCallIsError(t *testing.T, response mcp.JSONRPCMessage) {
	t.Helper()

	switch typed := response.(type) {
	case mcp.JSONRPCError:
		return // protocol-level error — acceptable
	case mcp.JSONRPCResponse:
		resultBytes, err := json.Marshal(typed.Result)
		if err != nil {
			t.Fatalf("marshaling tools/call result: %v", err)
		}

		var callResult mcp.CallToolResult
		if err := json.Unmarshal(resultBytes, &callResult); err != nil {
			t.Fatalf("decoding tools/call result: %v", err)
		}

		if !callResult.IsError {
			t.Fatalf("expected a tool-level error result, got a success result: %#v", callResult)
		}
	default:
		t.Fatalf("unexpected HandleMessage response type: %#v", response)
	}
}

// TestMCP_ConcurrentToolCalls covers "Two concurrent tool calls (via pipe)":
// two memory_start_conversation calls dispatched concurrently through the
// same server both complete successfully with distinct handles.
func TestMCP_ConcurrentToolCalls(t *testing.T) {
	b := newTestBridge(t)
	s := newTestMCPServer(b)

	results := make([]mcp.JSONRPCMessage, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func(index int) {
			defer wg.Done()
			request := jsonRPCRequest(t, index+1, string(mcp.MethodToolsCall), mcp.CallToolParams{
				Name: "memory_start_conversation",
			})
			results[index] = s.HandleMessage(context.Background(), request)
		}(i)
	}

	wg.Wait()

	handles := make(map[string]bool)
	for _, response := range results {
		rpcResponse, ok := response.(mcp.JSONRPCResponse)
		if !ok {
			t.Fatalf("expected a JSON-RPC response, got %#v", response)
		}

		resultBytes, err := json.Marshal(rpcResponse.Result)
		if err != nil {
			t.Fatalf("marshaling tools/call result: %v", err)
		}

		var callResult mcp.CallToolResult
		if err := json.Unmarshal(resultBytes, &callResult); err != nil {
			t.Fatalf("decoding tools/call result: %v", err)
		}
		if callResult.IsError {
			t.Fatalf("expected both concurrent calls to succeed, got an error: %#v", callResult)
		}

		text, ok := callResult.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %#v", callResult.Content[0])
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
			t.Fatalf("decoding memory_start_conversation response: %v", err)
		}

		handle, _ := decoded["handle"].(string)
		handles[handle] = true
	}

	if len(handles) != 2 {
		t.Fatalf("expected 2 distinct handles from 2 concurrent calls, got %d: %#v", len(handles), handles)
	}
}

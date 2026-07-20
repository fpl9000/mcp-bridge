// errors_test.go covers design spec Section 8.1.10: the error response
// convention.
package main

import (
	"strings"
	"testing"
)

// TestErrorConvention_Shape covers "Error shape": every failing memory tool
// call returns { handle, ok: false, error: { code, message } }. This uses a
// non-handle failure (block not found) so the handle field is expected to be
// populated — Section 3.19 specifies the handle is omitted specifically when
// the error itself concerns the handle (see TestGetCore_HandleEcho).
func TestErrorConvention_Shape(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "missing"})

	if response["ok"] != false {
		t.Fatalf("expected ok:false, got %#v", response)
	}
	if response["handle"] != handle {
		t.Fatalf("expected handle to be echoed, got %#v", response)
	}

	errorField, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error object, got %#v", response["error"])
	}
	if _, ok := errorField["code"].(string); !ok {
		t.Fatalf("expected error.code to be a string, got %#v", errorField)
	}
	if _, ok := errorField["message"].(string); !ok {
		t.Fatalf("expected error.message to be a string, got %#v", errorField)
	}
}

// TestErrorConvention_KnownCodes covers "Known error codes": every failure
// mode this build can trigger produces one of the codes in the design's
// reachable set (Section 3.19, minus MAINTENANCE_IN_PROGRESS, which belongs
// to the deferred maintenance feature).
func TestErrorConvention_KnownCodes(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	cases := []struct {
		name     string
		response map[string]any
		wantCode string
	}{
		{
			"malformed handle",
			callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "short"}),
			ErrCodeMalformedHandle,
		},
		{
			"invalid handle",
			callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "zzzzzzzz"}),
			ErrCodeInvalidHandle,
		},
		{
			"block not found",
			callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "missing"}),
			ErrCodeBlockNotFound,
		},
		{
			"invalid block name",
			callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "../escape"}),
			ErrCodeInvalidBlockName,
		},
		{
			"summary required",
			writeBlock(t, b, handle, "new-block", "content", ""),
			ErrCodeSummaryRequired,
		},
		{
			"summary too long",
			writeBlock(t, b, handle, "another-block", "content", strings.Repeat("x", b.Config.Memory.SummaryMaxLength+1)),
			ErrCodeSummaryTooLong,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertErrorCode(t, tc.response, tc.wantCode)
		})
	}
}

// TestErrorConvention_NoAbstractionLeaks covers "No abstraction leaks":
// error messages never mention branches, the mutex, frontmatter, or
// filesystem paths — every message stays at the handle/block/summary
// abstraction level the LLM operates on.
func TestErrorConvention_NoAbstractionLeaks(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	forbidden := []string{"branch", "mutex", "frontmatter", b.Config.Memory.Directory, ".tmp."}

	responses := []map[string]any{
		callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "short"}),
		callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "zzzzzzzz"}),
		callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "missing"}),
		callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "../escape"}),
		writeBlock(t, b, handle, "leak-check", "content", ""),
	}

	for _, response := range responses {
		errorField, ok := response["error"].(map[string]any)
		if !ok {
			t.Fatalf("expected an error object, got %#v", response)
		}

		message, _ := errorField["message"].(string)
		lowerMessage := strings.ToLower(message)

		for _, term := range forbidden {
			if term == "" {
				continue
			}
			if strings.Contains(lowerMessage, strings.ToLower(term)) {
				t.Fatalf("error message leaks implementation detail %q: %q", term, message)
			}
		}
	}
}

// TestErrorConvention_UniformRecovery covers "Uniform recovery":
// INVALID_HANDLE on any tool is recoverable by calling
// memory_start_conversation and retrying with the fresh handle.
func TestErrorConvention_UniformRecovery(t *testing.T) {
	b := newTestBridge(t)

	failure := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "zzzzzzzz"})
	assertErrorCode(t, failure, ErrCodeInvalidHandle)

	freshHandle := startConversation(t, b)

	retry := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": freshHandle})
	if retry["ok"] == false {
		t.Fatalf("expected retry with a fresh handle to succeed, got %#v", retry)
	}
}

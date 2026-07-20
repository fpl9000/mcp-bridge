// memory_core_test.go covers the core-related rows of design spec Sections
// 8.1.2 (memory_get_core) and 8.1.3 (memory_write_core).
package main

import (
	"testing"
	"time"
)

// TestGetCore_ReadsWrittenContent covers "Read core": a valid handle gets
// core.md's content back and records a read baseline.
func TestGetCore_ReadsWrittenContent(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryWriteCore, map[string]any{
		"handle":  handle,
		"content": "# Core\n\nFran is a retired principal software engineer.",
	})

	response := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handle})
	if content, _ := response["content"].(string); content != "# Core\n\nFran is a retired principal software engineer." {
		t.Fatalf("unexpected core content: %#v", response)
	}
}

// TestGetCore_UnknownHandle covers "Unknown handle": a well-formed but
// unregistered handle returns INVALID_HANDLE.
func TestGetCore_UnknownHandle(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "zzzzzzzz"})
	assertErrorCode(t, response, ErrCodeInvalidHandle)
}

// TestGetCore_MalformedHandle covers "Malformed handle": a handle not
// matching the required format returns MALFORMED_HANDLE.
func TestGetCore_MalformedHandle(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": "short"})
	assertErrorCode(t, response, ErrCodeMalformedHandle)
}

// TestGetCore_ChangedSinceLastRead covers the three changed_since_last_read
// cases for core: false on first read, false when unchanged, true after
// another handle writes.
func TestGetCore_ChangedSinceLastRead(t *testing.T) {
	b := newTestBridge(t)
	handleA := startConversation(t, b)
	handleB := startConversation(t, b)

	// First read: always false, even against an empty/cold-start core.
	first := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handleA})
	if first["changed_since_last_read"] != false {
		t.Fatalf("expected changed_since_last_read=false on first read, got %#v", first)
	}

	// Unchanged re-read: still false.
	second := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handleA})
	if second["changed_since_last_read"] != false {
		t.Fatalf("expected changed_since_last_read=false on unchanged re-read, got %#v", second)
	}

	// Handle B writes core; handle A's next read must report the change.
	// The changed_since_last_read signal is ModTime+size, so a short sleep
	// guarantees this write's signature is distinguishable from the first.
	time.Sleep(10 * time.Millisecond)
	callToolJSON(t, b.HandleMemoryWriteCore, map[string]any{"handle": handleB, "content": "Updated by B"})

	third := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handleA})
	if third["changed_since_last_read"] != true {
		t.Fatalf("expected changed_since_last_read=true after another handle's write, got %#v", third)
	}
}

// TestGetCore_ReadYourOwnWrites covers "Read-your-own-writes" for core: a
// handle that writes core and then reads it sees its own content with
// changed_since_last_read: false.
func TestGetCore_ReadYourOwnWrites(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryWriteCore, map[string]any{"handle": handle, "content": "My own update"})

	response := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handle})
	if content, _ := response["content"].(string); content != "My own update" {
		t.Fatalf("expected to see own write, got %#v", response)
	}
	if response["changed_since_last_read"] != false {
		t.Fatalf("expected changed_since_last_read=false reading own write, got %#v", response)
	}
}

// TestGetCore_HandleEcho covers "Handle echo": every response — success or
// failure — includes the handle field, EXCEPT when the error itself is a
// handle problem (INVALID_HANDLE / MALFORMED_HANDLE), which Section 3.19
// explicitly specifies must omit or null the handle rather than echo back
// something the bridge just rejected.
func TestGetCore_HandleEcho(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	success := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handle})
	if success["handle"] != handle {
		t.Fatalf("success response missing handle echo: %#v", success)
	}

	// A non-handle failure (block not found) must still echo the valid
	// handle that made the call.
	failure := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "missing"})
	if failure["handle"] != handle {
		t.Fatalf("non-handle failure response missing handle echo: %#v", failure)
	}
}

// TestWriteCore_NoFrontmatter covers "Write core": core.md is replaced
// atomically with no frontmatter added — the raw file on disk should be
// exactly the supplied content, verbatim.
func TestWriteCore_NoFrontmatter(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	content := "---\nthis looks like frontmatter but isn't\n---\nBody text"
	callToolJSON(t, b.HandleMemoryWriteCore, map[string]any{"handle": handle, "content": content})

	raw := readFileString(t, b.Config.CorePath())
	if raw != content {
		t.Fatalf("core.md was not written verbatim:\ngot:  %q\nwant: %q", raw, content)
	}
}

// TestWriteCore_UnknownHandleDoesNotWrite covers "Unknown handle" for
// memory_write_core: an unregistered handle is rejected and no file is
// modified.
func TestWriteCore_UnknownHandleDoesNotWrite(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryWriteCore, map[string]any{"handle": "zzzzzzzz", "content": "should not be written"})
	assertErrorCode(t, response, ErrCodeInvalidHandle)

	if fileExists(b.Config.CorePath()) {
		t.Fatalf("core.md should not exist after a rejected write")
	}
}

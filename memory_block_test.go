// memory_block_test.go covers the block-related rows of design spec Sections
// 8.1.2 (memory_get_block), 8.1.3 (memory_write_block), and 8.1.4
// (memory_append_block), plus the atomic-write cleanup behavior underlying
// Section 3.16.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeBlock is a small test convenience wrapping memory_write_block.
func writeBlock(t *testing.T, b *Bridge, handle, name, content, summary string) map[string]any {
	t.Helper()

	args := map[string]any{"handle": handle, "block_name": name, "content": content}
	if summary != "" {
		args["summary"] = summary
	}

	return callToolJSON(t, b.HandleMemoryWriteBlock, args)
}

// TestWriteBlock_CreatesWithFrontmatter covers "Write new block": the file
// is created with bridge-generated YAML frontmatter (summary, updated_at).
func TestWriteBlock_CreatesWithFrontmatter(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := writeBlock(t, b, handle, "project-foo", "# Project Foo\n\nDetails.", "Discussion of project foo")
	if response["ok"] != true {
		t.Fatalf("expected ok:true, got %#v", response)
	}

	raw := readFileString(t, b.blockPath("project-foo"))
	if !strings.HasPrefix(raw, "---\n") {
		t.Fatalf("expected block file to start with frontmatter delimiter, got: %q", raw)
	}
	if !strings.Contains(raw, "summary: Discussion of project foo") {
		t.Fatalf("expected frontmatter to contain the summary, got: %q", raw)
	}
	if !strings.Contains(raw, "updated_at:") {
		t.Fatalf("expected frontmatter to contain updated_at, got: %q", raw)
	}
}

// TestWriteBlock_NewBlockWithoutSummary covers "New block without summary":
// SUMMARY_REQUIRED.
func TestWriteBlock_NewBlockWithoutSummary(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := writeBlock(t, b, handle, "project-foo", "content", "")
	assertErrorCode(t, response, ErrCodeSummaryRequired)
}

// TestWriteBlock_SummaryTooLong covers "Summary too long": a summary
// exceeding memory.summary_max_length returns SUMMARY_TOO_LONG.
func TestWriteBlock_SummaryTooLong(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	longSummary := strings.Repeat("x", b.Config.Memory.SummaryMaxLength+1)
	response := writeBlock(t, b, handle, "project-foo", "content", longSummary)
	assertErrorCode(t, response, ErrCodeSummaryTooLong)
}

// TestWriteBlock_UpdateWithoutSummaryPreservesIt covers "Update existing
// block, no summary": the existing frontmatter summary is preserved and
// updated_at is refreshed.
func TestWriteBlock_UpdateWithoutSummaryPreservesIt(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "v1", "Original summary")
	firstRaw := readFileString(t, b.blockPath("project-foo"))

	// Update without a summary argument at all.
	response := callToolJSON(t, b.HandleMemoryWriteBlock, map[string]any{
		"handle": handle, "block_name": "project-foo", "content": "v2",
	})
	if response["ok"] != true {
		t.Fatalf("expected ok:true, got %#v", response)
	}

	secondRaw := readFileString(t, b.blockPath("project-foo"))
	if !strings.Contains(secondRaw, "summary: Original summary") {
		t.Fatalf("expected original summary to be preserved, got: %q", secondRaw)
	}
	if secondRaw == firstRaw {
		t.Fatalf("expected updated_at to be refreshed on the second write")
	}
}

// TestWriteBlock_UpdateWithNewSummaryReplacesIt covers "Update existing
// block, new summary": the frontmatter summary is replaced.
func TestWriteBlock_UpdateWithNewSummaryReplacesIt(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "v1", "Original summary")
	writeBlock(t, b, handle, "project-foo", "v2", "New summary")

	raw := readFileString(t, b.blockPath("project-foo"))
	if !strings.Contains(raw, "summary: New summary") {
		t.Fatalf("expected replaced summary, got: %q", raw)
	}
	if strings.Contains(raw, "Original summary") {
		t.Fatalf("expected original summary to be gone, got: %q", raw)
	}
}

// TestWriteBlock_UnknownHandleDoesNotWrite covers "Unknown handle": an
// unregistered handle is rejected and no file is created.
func TestWriteBlock_UnknownHandleDoesNotWrite(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryWriteBlock, map[string]any{
		"handle": "zzzzzzzz", "block_name": "project-foo", "content": "x", "summary": "s",
	})
	assertErrorCode(t, response, ErrCodeInvalidHandle)

	if fileExists(b.blockPath("project-foo")) {
		t.Fatalf("block file should not exist after a rejected write")
	}
}

// TestWriteBlock_FrontmatterIsBridgePrivate covers "Frontmatter is
// bridge-private": content that itself starts with "---" is stored verbatim
// in the body, and the bridge's own frontmatter stays well-formed and
// separate.
func TestWriteBlock_FrontmatterIsBridgePrivate(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	trickyContent := "---\nthis is body content that looks like frontmatter\n---\nMore body."
	writeBlock(t, b, handle, "project-foo", trickyContent, "A tricky block")

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "project-foo"})
	if content, _ := response["content"].(string); content != trickyContent {
		t.Fatalf("expected body to round-trip verbatim, got: %q", content)
	}
}

// TestGetBlock_FrontmatterStrippedAndBaselineRecorded covers "Read existing
// block" and "Frontmatter stripped on read".
func TestGetBlock_FrontmatterStrippedAndBaselineRecorded(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "# Body\n\nNo frontmatter should appear here.", "A project")

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "project-foo"})
	content, _ := response["content"].(string)

	if strings.Contains(content, "---") {
		t.Fatalf("expected frontmatter to be stripped from returned content, got: %q", content)
	}
	if content != "# Body\n\nNo frontmatter should appear here." {
		t.Fatalf("unexpected block body: %q", content)
	}

	if _, hadBaseline := b.Handles.Baseline(handle, "project-foo"); !hadBaseline {
		t.Fatalf("expected a read baseline to be recorded for project-foo")
	}
}

// TestGetBlock_NotFound covers "Read non-existent block": BLOCK_NOT_FOUND.
func TestGetBlock_NotFound(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "does-not-exist"})
	assertErrorCode(t, response, ErrCodeBlockNotFound)
}

// TestGetBlock_InvalidName covers "Invalid block name": names with path
// separators or ".." are rejected with INVALID_BLOCK_NAME. Because block
// names are never interpreted as paths, this is a pure syntax check, not a
// path-traversal exploit test.
func TestGetBlock_InvalidName(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	for _, badName := range []string{"../escape", "sub/dir", "sub\\dir", "..", ""} {
		t.Run(badName, func(t *testing.T) {
			response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": badName})
			assertErrorCode(t, response, ErrCodeInvalidBlockName)
		})
	}
}

// TestGetBlock_ChangedSinceLastRead covers the changed_since_last_read cases
// for blocks: false on first read, false when unchanged, true after another
// handle writes.
func TestGetBlock_ChangedSinceLastRead(t *testing.T) {
	b := newTestBridge(t)
	handleA := startConversation(t, b)
	handleB := startConversation(t, b)

	writeBlock(t, b, handleA, "shared", "v1", "A shared block")

	first := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handleA, "block_name": "shared"})
	if first["changed_since_last_read"] != false {
		t.Fatalf("expected false on first read, got %#v", first)
	}

	second := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handleA, "block_name": "shared"})
	if second["changed_since_last_read"] != false {
		t.Fatalf("expected false on unchanged re-read, got %#v", second)
	}

	// The changed_since_last_read signal is ModTime+size (Section 3.14 notes
	// a content hash as a possible future upgrade), so a short sleep
	// guarantees this write's signature is distinguishable from the first.
	time.Sleep(10 * time.Millisecond)
	writeBlock(t, b, handleB, "shared", "v2", "")

	third := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handleA, "block_name": "shared"})
	if third["changed_since_last_read"] != true {
		t.Fatalf("expected true after another handle's write, got %#v", third)
	}
}

// TestGetBlock_ReadYourOwnWrites covers "Read-your-own-writes" for blocks.
func TestGetBlock_ReadYourOwnWrites(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "my content", "A project")

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "project-foo"})
	if content, _ := response["content"].(string); content != "my content" {
		t.Fatalf("expected to see own write, got %#v", response)
	}
	if response["changed_since_last_read"] != false {
		t.Fatalf("expected changed_since_last_read=false reading own write, got %#v", response)
	}
}

// TestAppendBlock_AppendsToExisting covers "Append to existing block": text
// is appended after the existing body, and frontmatter is untouched except
// updated_at.
func TestAppendBlock_AppendsToExisting(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "log", "first line", "A log")
	response := callToolJSON(t, b.HandleMemoryAppendBlock, map[string]any{
		"handle": handle, "block_name": "log", "content": "\nsecond line",
	})
	if response["ok"] != true {
		t.Fatalf("expected ok:true, got %#v", response)
	}

	getResponse := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "log"})
	if content, _ := getResponse["content"].(string); content != "first line\nsecond line" {
		t.Fatalf("unexpected appended content: %q", content)
	}

	raw := readFileString(t, b.blockPath("log"))
	if !strings.Contains(raw, "summary: A log") {
		t.Fatalf("expected summary to survive append untouched, got: %q", raw)
	}
}

// TestAppendBlock_NotFound covers "Append to non-existent block":
// BLOCK_NOT_FOUND (appends never create blocks).
func TestAppendBlock_NotFound(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryAppendBlock, map[string]any{
		"handle": handle, "block_name": "does-not-exist", "content": "text",
	})
	assertErrorCode(t, response, ErrCodeBlockNotFound)
}

// TestAppendBlock_EmptyTextIsNoOp covers "Empty text": success, no write, and
// updated_at unchanged.
func TestAppendBlock_EmptyTextIsNoOp(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "log", "content", "A log")
	before := readFileString(t, b.blockPath("log"))

	response := callToolJSON(t, b.HandleMemoryAppendBlock, map[string]any{
		"handle": handle, "block_name": "log", "content": "",
	})
	if response["ok"] != true {
		t.Fatalf("expected ok:true for an empty append, got %#v", response)
	}

	after := readFileString(t, b.blockPath("log"))
	if before != after {
		t.Fatalf("expected file to be byte-for-byte unchanged after an empty append")
	}
}

// TestAtomicWriteFile_CleansUpTempFileOnFailure covers "Temp file cleanup on
// failure" (Section 8.1.3): when the underlying write cannot complete, no
// temp file is left behind.
func TestAtomicWriteFile_CleansUpTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "block.md")

	// Make the "target" a non-empty directory so atomicWriteFile's
	// os.Remove(path) step fails, forcing it down its cleanup path.
	if err := os.Mkdir(targetPath, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "occupant.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := atomicWriteFile(targetPath, []byte("new content"), 0o644); err == nil {
		t.Fatalf("expected atomicWriteFile to fail when the target is a non-empty directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading test dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("temp file %q was not cleaned up after a failed write", entry.Name())
		}
	}
}

// memory_index_test.go covers design spec Section 8.1.5 (memory_get_index),
// dropping the "reflects own branch" row per IMPLEMENTATION-PROMPT-minimal.md
// Section 2 (branching omitted from this build).
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// indexBlocks extracts the index.blocks array from a decoded
// memory_get_index response as a slice of maps, for convenient assertions.
func indexBlocks(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()

	index, ok := response["index"].(map[string]any)
	if !ok {
		t.Fatalf("expected index object, got %#v", response["index"])
	}

	rawBlocks, ok := index["blocks"].([]any)
	if !ok {
		t.Fatalf("expected index.blocks array, got %#v", index["blocks"])
	}

	blocks := make([]map[string]any, len(rawBlocks))
	for i, entry := range rawBlocks {
		blocks[i], _ = entry.(map[string]any)
	}

	return blocks
}

// TestGetIndex_EmptyBlocksDirectory covers "Empty blocks directory": returns
// an empty index array.
func TestGetIndex_EmptyBlocksDirectory(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, response)
	if len(blocks) != 0 {
		t.Fatalf("expected an empty index, got %d entries", len(blocks))
	}
}

// TestGetIndex_ReflectsFrontmatterAndSortedByName covers "Index reflects
// frontmatter" and "Sorted by name": three blocks created out of order come
// back sorted lexicographically with exact summary/updated_at values.
func TestGetIndex_ReflectsFrontmatterAndSortedByName(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "zeta", "content z", "Summary Z")
	writeBlock(t, b, handle, "alpha", "content a", "Summary A")
	writeBlock(t, b, handle, "mu", "content m", "Summary M")

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, response)

	if len(blocks) != 3 {
		t.Fatalf("expected 3 index entries, got %d: %#v", len(blocks), blocks)
	}

	gotNames := []string{blocks[0]["name"].(string), blocks[1]["name"].(string), blocks[2]["name"].(string)}
	wantNames := []string{"alpha", "mu", "zeta"}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("expected sorted names %v, got %v", wantNames, gotNames)
		}
	}

	if blocks[0]["summary"] != "Summary A" {
		t.Fatalf("expected alpha's summary to match frontmatter, got %#v", blocks[0])
	}
}

// TestGetIndex_TimestampsAreExtendedISO8601 covers "Timestamps in extended
// ISO 8601": updated_at values look like 2026-05-20T14:23:00Z.
func TestGetIndex_TimestampsAreExtendedISO8601(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "content", "A project")

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, response)

	updatedAt, _ := blocks[0]["updated_at"].(string)
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		t.Fatalf("expected updated_at %q to parse as extended ISO 8601 (RFC3339): %v", updatedAt, err)
	}
}

// TestGetIndex_NoIndexFileOnDisk covers "No index file on disk": the index
// is purely derived — no index.md or similar file is ever created.
func TestGetIndex_NoIndexFileOnDisk(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "content", "A project")
	callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})

	if fileExists(b.blockPath("index")) {
		t.Fatalf("no index file should ever be created on disk")
	}
}

// TestGetIndex_BridgeStateFileExcluded covers "Bridge state file excluded":
// .bridge-state.json never appears in the index (it lives in the memory
// root, not the blocks directory, so it is excluded structurally).
func TestGetIndex_BridgeStateFileExcluded(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "content", "A project")
	if err := b.Persist.WriteCheckpoint(); err != nil {
		t.Fatalf("WriteCheckpoint failed: %v", err)
	}

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	for _, block := range indexBlocks(t, response) {
		if block["name"] == ".bridge-state" {
			t.Fatalf("bridge state file leaked into the index: %#v", block)
		}
	}
}

// TestGetIndex_MissingFrontmatterTolerated covers "Missing frontmatter
// tolerated": a hand-created block with no frontmatter gets a placeholder
// summary and no error, preserving user-editable transparency.
func TestGetIndex_MissingFrontmatterTolerated(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	handEditedPath := b.blockPath("hand-edited")
	if err := os.WriteFile(handEditedPath, []byte("# Hand Edited\n\nNo frontmatter here."), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, response)

	found := false
	for _, block := range blocks {
		if block["name"] == "hand-edited" {
			found = true
			if block["summary"] == "" || block["summary"] == nil {
				t.Fatalf("expected a derived placeholder summary, got %#v", block)
			}
		}
	}
	if !found {
		t.Fatalf("expected hand-edited block to appear in the index despite missing frontmatter")
	}
}

// TestGetIndex_CacheInvalidatedOnWrite covers "Per-handle cache correctness":
// reading the index, writing a block, then reading again reflects the write.
func TestGetIndex_CacheInvalidatedOnWrite(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	first := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	if len(indexBlocks(t, first)) != 0 {
		t.Fatalf("expected empty index before any writes")
	}

	writeBlock(t, b, handle, "project-foo", "content", "A project")

	second := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, second)
	if len(blocks) != 1 || blocks[0]["name"] != "project-foo" {
		t.Fatalf("expected the index to reflect the new block, got %#v", blocks)
	}
}

// TestGetIndex_CacheDetectsInPlaceExternalEdit covers the case where the user
// edits a block file directly with a text editor rather than going through the
// bridge. Keying the cache on the blocks directory's own mtime is not
// sufficient for this: on every mainstream filesystem a directory's mtime
// tracks changes to its set of entries, not to the contents of the files those
// entries name. An editor configured to write in place — for example Emacs with
// `backup-by-copying` set to t, which copies the original aside and then
// rewrites the original file — therefore leaves the directory mtime untouched,
// and a directory-keyed cache would keep serving the pre-edit index.
//
// The edit here changes the frontmatter summary, because that is the field an
// external edit can change without the bridge ever learning about it. The file
// is rewritten through its existing directory entry (no create, rename, or
// remove), which is precisely what an in-place editor save does.
func TestGetIndex_CacheDetectsInPlaceExternalEdit(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	writeBlock(t, b, handle, "project-foo", "body text", "The original summary")

	// This first call populates the cache, which is what makes the staleness
	// possible at all; without it the next call would reassemble regardless.
	before := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	if blocks := indexBlocks(t, before); len(blocks) != 1 || blocks[0]["summary"] != "The original summary" {
		t.Fatalf("expected the original summary to be cached, got %#v", blocks)
	}

	blockPath := filepath.Join(b.Config.BlocksDirectory(), "project-foo.md")

	directoryModTimeBefore := directoryModTime(t, b.Config.BlocksDirectory())

	edited := "---\nsummary: An externally edited summary\nupdated_at: 2026-07-30T00:00:00Z\n---\n\nbody text\n"
	if err := os.WriteFile(blockPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("simulating an external in-place edit: %v", err)
	}

	// Advance the file's mtime deliberately rather than relying on the clock to
	// have ticked between the bridge's write and this one. Coarse filesystem
	// timestamp granularity would otherwise make this test flaky.
	editTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(blockPath, editTime, editTime); err != nil {
		t.Fatalf("advancing the edited file's mtime: %v", err)
	}

	// The premise of the test is that the directory itself looks unchanged. If
	// this ever fails, the test has stopped exercising the case it was written
	// for and would pass for the wrong reason.
	if directoryModTimeAfter := directoryModTime(t, b.Config.BlocksDirectory()); !directoryModTimeAfter.Equal(directoryModTimeBefore) {
		t.Skipf("this filesystem moves directory mtime on an in-place file write, so the stale-cache case cannot be reproduced here")
	}

	after := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	blocks := indexBlocks(t, after)
	if len(blocks) != 1 {
		t.Fatalf("expected exactly one block after the edit, got %#v", blocks)
	}

	if blocks[0]["summary"] != "An externally edited summary" {
		t.Fatalf("index served a stale summary after an in-place external edit: got %#v", blocks[0]["summary"])
	}
}

// directoryModTime reads a directory's own mtime, used to assert the premise of
// the in-place-edit test above.
func directoryModTime(t *testing.T, dir string) time.Time {
	t.Helper()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat of blocks directory: %v", err)
	}

	return info.ModTime()
}

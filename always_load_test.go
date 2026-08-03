package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// alwaysLoadedFrom pulls the always_loaded array out of a decoded
// memory_start_conversation response, as a slice of maps.
func alwaysLoadedFrom(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()

	raw, present := response["always_loaded"]
	if !present {
		return nil
	}

	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("always_loaded was not an array: %#v", raw)
	}

	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item, itemOk := entry.(map[string]any)
		if !itemOk {
			t.Fatalf("always_loaded entry was not an object: %#v", entry)
		}
		out = append(out, item)
	}

	return out
}

// TestAlwaysLoad_OmittedWhenUnconfigured covers the default case, which is
// every fresh installation: with no always_load_blocks configured, the
// response must be byte-identical in shape to what it was before the feature
// existed, so that nothing downstream has to know the feature is there.
func TestAlwaysLoad_OmittedWhenUnconfigured(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)

	if _, present := response["always_loaded"]; present {
		t.Fatalf("always_loaded should be absent when unconfigured, got %#v", response["always_loaded"])
	}
}

// TestAlwaysLoad_ReturnsBodyWithoutFrontmatter checks that an always-loaded
// block arrives in the same form memory_get_block would return it: the body
// only, with YAML frontmatter stripped. If the two differed, the caller would
// have to care how the block reached it.
func TestAlwaysLoad_ReturnsBodyWithoutFrontmatter(t *testing.T) {
	b := newTestBridge(t)
	b.Config.Memory.AlwaysLoadBlocks = []string{"conventions"}

	seed := startConversation(t, b)
	writeBlock(t, b, seed, "conventions", "always indent with spaces", "House conventions")

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)
	entries := alwaysLoadedFrom(t, response)

	if len(entries) != 1 {
		t.Fatalf("expected exactly one always-loaded block, got %#v", entries)
	}

	if entries[0]["name"] != "conventions" {
		t.Fatalf("wrong block name: %#v", entries[0]["name"])
	}

	content, _ := entries[0]["content"].(string)
	if content != "always indent with spaces" {
		t.Fatalf("content should be the body with frontmatter stripped, got %q", content)
	}

	if skipped, _ := entries[0]["skipped"].(bool); skipped {
		t.Fatalf("block exists and should not be reported as skipped: %#v", entries[0])
	}
}

// TestAlwaysLoad_MissingBlockIsSkippedNotFatal covers the fresh-installation
// case and the configure-before-creating case. A named block that does not
// exist must not fail conversation startup -- but it must be visible, not
// silently dropped, or a typo would look identical to a working config.
func TestAlwaysLoad_MissingBlockIsSkippedNotFatal(t *testing.T) {
	b := newTestBridge(t)
	b.Config.Memory.AlwaysLoadBlocks = []string{"not-created-yet"}

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)

	if handle, _ := response["handle"].(string); handle == "" {
		t.Fatalf("startup must still succeed when an always-load block is missing: %#v", response)
	}

	entries := alwaysLoadedFrom(t, response)
	if len(entries) != 1 {
		t.Fatalf("expected the missing block to be reported, got %#v", entries)
	}

	if skipped, _ := entries[0]["skipped"].(bool); !skipped {
		t.Fatalf("missing block should be marked skipped: %#v", entries[0])
	}

	if reason, _ := entries[0]["reason"].(string); reason == "" {
		t.Fatalf("a skipped block must carry a reason: %#v", entries[0])
	}
}

// TestAlwaysLoad_EstablishesReadBaseline is the subtle one. Loading a block
// must record a read baseline exactly as memory_get_block does. Without it,
// the caller holds the content but the bridge does not know that, so the next
// memory_get_block would report changed_since_last_read as false -- masking a
// genuine external edit made in between.
func TestAlwaysLoad_EstablishesReadBaseline(t *testing.T) {
	b := newTestBridge(t)
	b.Config.Memory.AlwaysLoadBlocks = []string{"conventions"}

	seed := startConversation(t, b)
	writeBlock(t, b, seed, "conventions", "original text", "House conventions")

	// This handle receives the block via always-load, never via get_block.
	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)
	handle, _ := response["handle"].(string)

	blockPath := filepath.Join(b.Config.BlocksDirectory(), "conventions.md")
	edited := "---\nsummary: House conventions\nupdated_at: 2026-08-03T00:00:00Z\n---\n\nedited outside the bridge\n"
	if err := os.WriteFile(blockPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("simulating an external edit: %v", err)
	}

	// Advance mtime explicitly rather than relying on the clock to have
	// ticked, which coarse filesystem timestamps would make flaky.
	editTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(blockPath, editTime, editTime); err != nil {
		t.Fatalf("advancing mtime: %v", err)
	}

	got := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "conventions"})
	changed, _ := got["changed_since_last_read"].(bool)
	if !changed {
		t.Fatalf("always-load must set a baseline so a later external edit is reported; got %#v", got)
	}
}

// TestAlwaysLoad_SizeCapSkipsRatherThanTruncates checks that exceeding the cap
// omits whole blocks and says so, rather than delivering a half block. A
// truncated conventions document is worse than an absent one, because it looks
// complete.
func TestAlwaysLoad_SizeCapSkipsRatherThanTruncates(t *testing.T) {
	b := newTestBridge(t)
	b.Config.Memory.AlwaysLoadBlocks = []string{"small", "huge"}
	b.Config.Memory.AlwaysLoadMaxBytes = 64

	seed := startConversation(t, b)
	writeBlock(t, b, seed, "small", "tiny body", "Small block")
	writeBlock(t, b, seed, "huge", longBody(200), "Huge block")

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)
	entries := alwaysLoadedFrom(t, response)

	if len(entries) != 2 {
		t.Fatalf("expected both blocks reported, got %#v", entries)
	}

	if content, _ := entries[0]["content"].(string); content != "tiny body" {
		t.Fatalf("the block that fits should be loaded intact, got %q", content)
	}

	if skipped, _ := entries[1]["skipped"].(bool); !skipped {
		t.Fatalf("the oversized block should be skipped: %#v", entries[1])
	}

	if content, present := entries[1]["content"]; present && content != "" {
		t.Fatalf("a skipped block must carry no partial content: %#v", content)
	}
}

// TestAlwaysLoad_InvalidNameRejectedAtConfigLoad checks that a malformed name
// fails validation rather than being skipped at runtime. A typo that merely
// skips would leave the operator believing the block was loaded for as long as
// they never checked.
func TestAlwaysLoad_InvalidNameRejectedAtConfigLoad(t *testing.T) {
	dir := t.TempDir()

	cfg := &Config{
		Memory: MemoryConfig{
			Directory:        dir,
			SummaryMaxLength: 200,
			AlwaysLoadBlocks: []string{"../escape"},
		},
		Handle:      HandleConfig{IDLength: 8, RetentionDays: 60},
		Persistence: PersistenceConfig{StateFile: ".bridge-state.json", CheckpointIntervalSeconds: 1},
		Logging:     LoggingConfig{File: filepath.Join(dir, "bridge.log"), Level: "debug"},
	}

	if err := cfg.validate(); err == nil {
		t.Fatal("a block name that could escape the blocks directory must be rejected at config load")
	}
}

// longBody returns a body of the given length, used to exceed the size cap.
func longBody(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'x'
	}

	return string(buf)
}

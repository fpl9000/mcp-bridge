// memory_episodic_test.go covers the episodic-log rows of design spec
// Section 8.1.4.
package main

import (
	"strings"
	"testing"
	"time"
)

// TestAppendEpisodic_CreatesMonthlyFile covers "Episodic append creates
// monthly file": the first append of the month creates episodic-YYYY-MM.md
// with a bridge-generated summary.
func TestAppendEpisodic_CreatesMonthlyFile(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "First entry", "content": "Something happened.",
	})
	if response["ok"] != true {
		t.Fatalf("expected ok:true, got %#v", response)
	}

	expectedName := episodicBlockName(time.Now())
	if !fileExists(b.blockPath(expectedName)) {
		t.Fatalf("expected monthly file %q to be created", expectedName)
	}

	raw := readFileString(t, b.blockPath(expectedName))
	if !strings.Contains(raw, "summary: Conversation log for") {
		t.Fatalf("expected bridge-generated summary, got: %q", raw)
	}
}

// TestAppendEpisodic_SubsequentAppendsShareTheSameFile covers the same-month
// half of "Episodic month rotation": two appends in the same month land in
// the same file, in order.
func TestAppendEpisodic_SubsequentAppendsShareTheSameFile(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry one", "content": "First."})
	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry two", "content": "Second."})

	monthName := episodicBlockName(time.Now())
	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": monthName})
	content, _ := response["content"].(string)

	firstIndex := strings.Index(content, "Entry one")
	secondIndex := strings.Index(content, "Entry two")
	if firstIndex == -1 || secondIndex == -1 || firstIndex > secondIndex {
		t.Fatalf("expected both entries present and in order, got: %q", content)
	}
}

// TestEpisodicBlockName_RotatesAcrossMonths covers the naming half of
// "Episodic month rotation" directly: two different months produce two
// different block names.
func TestEpisodicBlockName_RotatesAcrossMonths(t *testing.T) {
	may := time.Date(2026, time.May, 15, 12, 0, 0, 0, time.UTC)
	june := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

	if got := episodicBlockName(may); got != "episodic-2026-05" {
		t.Fatalf("expected episodic-2026-05, got %q", got)
	}
	if got := episodicBlockName(june); got != "episodic-2026-06" {
		t.Fatalf("expected episodic-2026-06, got %q", got)
	}
}

// TestAppendEpisodic_RefreshesUpdatedAt covers "Episodic entries timestamped":
// each append refreshes the file's updated_at frontmatter.
func TestAppendEpisodic_RefreshesUpdatedAt(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry one", "content": "First."})
	monthName := episodicBlockName(time.Now())

	firstRaw := readFileString(t, b.blockPath(monthName))
	fm, _, _ := splitFrontmatter([]byte(firstRaw))
	firstUpdatedAt := fm.UpdatedAt

	time.Sleep(10 * time.Millisecond)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry two", "content": "Second."})
	secondRaw := readFileString(t, b.blockPath(monthName))
	fm2, _, _ := splitFrontmatter([]byte(secondRaw))

	if !fm2.UpdatedAt.After(firstUpdatedAt) {
		t.Fatalf("expected updated_at to advance after the second append: first=%v second=%v", firstUpdatedAt, fm2.UpdatedAt)
	}
}

// TestAppendEpisodic_BlockIsIndexed covers "Episodic blocks indexed": after
// the first append, memory_get_index includes the episodic block.
func TestAppendEpisodic_BlockIsIndexed(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry", "content": "Details."})
	monthName := episodicBlockName(time.Now())

	response := callToolJSON(t, b.HandleMemoryGetIndex, map[string]any{"handle": handle})
	index, _ := response["index"].(map[string]any)
	blocks, _ := index["blocks"].([]any)

	found := false
	for _, entry := range blocks {
		row, _ := entry.(map[string]any)
		if row["name"] == monthName {
			found = true
			if summary, _ := row["summary"].(string); !strings.HasPrefix(summary, "Conversation log for") {
				t.Fatalf("expected bridge-generated summary in index, got %q", summary)
			}
		}
	}
	if !found {
		t.Fatalf("expected %q to appear in the index, got %#v", monthName, blocks)
	}
}

// TestAppendEpisodic_ReadableAsBlock covers "Episodic readable as block":
// memory_get_block on the episodic name returns the log body with
// frontmatter stripped.
func TestAppendEpisodic_ReadableAsBlock(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{"handle": handle, "title": "Entry", "content": "Details."})
	monthName := episodicBlockName(time.Now())

	response := callToolJSON(t, b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": monthName})
	content, _ := response["content"].(string)

	if strings.Contains(content, "---") {
		t.Fatalf("expected frontmatter to be stripped, got: %q", content)
	}
	if !strings.Contains(content, "Entry") || !strings.Contains(content, "Details.") {
		t.Fatalf("expected entry content in body, got: %q", content)
	}
}

// TestAppendEpisodic_HeadingSurvivesBodyWithoutTrailingNewline is the
// regression test for the defect that motivated bridge-composed headings. A
// prior entry whose body did not end with a newline used to absorb the next
// entry's heading onto its final line, where a "##" no longer at column zero
// stops being a heading at all.
func TestAppendEpisodic_HeadingSurvivesBodyWithoutTrailingNewline(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	// "No trailing newline." is written verbatim, so the block body ends
	// mid-line — exactly the state that used to swallow the next heading.
	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "First", "content": "No trailing newline."})
	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "Second", "content": "Body of the second entry."})

	monthName := episodicBlockName(time.Now())
	response := callToolJSON(t, b.HandleMemoryGetBlock,
		map[string]any{"handle": handle, "block_name": monthName})
	content, _ := response["content"].(string)

	// The second heading must begin a line of its own rather than trailing
	// the previous entry's text.
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") && strings.HasSuffix(line, "— Second") {
			return
		}
	}
	t.Fatalf("expected the second entry's heading to start its own line, got: %q", content)
}

// TestAppendEpisodic_BridgeComposesDatedHeading covers the heading format: the
// caller supplies only a title, and the bridge renders the date from its own
// clock so the heading can never disagree with the month the entry is filed
// under.
func TestAppendEpisodic_BridgeComposesDatedHeading(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "Composed heading", "content": "Body text."})

	monthName := episodicBlockName(time.Now())
	response := callToolJSON(t, b.HandleMemoryGetBlock,
		map[string]any{"handle": handle, "block_name": monthName})
	content, _ := response["content"].(string)

	expected := "## " + time.Now().Format("2006-01-02") + " — Composed heading"
	if !strings.Contains(content, expected) {
		t.Fatalf("expected heading %q, got: %q", expected, content)
	}
}

// TestAppendEpisodic_RejectsContentWithHeading covers the guard against a
// caller that still follows the old convention of embedding its own heading,
// which would otherwise produce two headings for a single entry.
func TestAppendEpisodic_RejectsContentWithHeading(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "Title", "content": "## 2026-07-29 — Old style\n\nBody."})

	errorDetail, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error response, got %#v", response)
	}
	if errorDetail["code"] != ErrCodeInvalidContent {
		t.Fatalf("expected code %q, got %#v", ErrCodeInvalidContent, errorDetail["code"])
	}
}

// TestAppendEpisodic_RejectsEmptyTitle covers the blank-title case, which
// would otherwise render as a date followed by a dangling separator.
func TestAppendEpisodic_RejectsEmptyTitle(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)

	response := callToolJSON(t, b.HandleMemoryAppendEpisodic, map[string]any{
		"handle": handle, "title": "   ", "content": "Body."})

	errorDetail, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error response, got %#v", response)
	}
	if errorDetail["code"] != ErrCodeInvalidContent {
		t.Fatalf("expected code %q, got %#v", ErrCodeInvalidContent, errorDetail["code"])
	}
}

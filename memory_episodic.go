// memory_episodic.go implements the memory_append_episodic tool handler,
// per design spec Section 3.12. The bridge computes the current month's
// episodic block name from the system clock and creates that month's file
// automatically at rollover — the LLM never computes a filename or block
// name for episodic entries.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// episodicBlockName returns the current month's episodic block name (e.g.
// "episodic-2026-06") from the system clock, per Section 3.12.
func episodicBlockName(now time.Time) string {
	return fmt.Sprintf("episodic-%04d-%02d", now.Year(), int(now.Month()))
}

// composeEpisodicEntry builds a complete episodic entry: a bridge-generated
// "## YYYY-MM-DD — Title" heading followed by the caller's body text.
//
// The heading is composed here rather than accepted from the caller for two
// reasons. First, it makes the entry format an invariant of the storage layer
// instead of a convention the calling model has to remember on every append,
// which is the failure this replaces. Second, it makes the bridge's clock the
// single authority for the date. The bridge already reads that clock to decide
// which monthly block the entry is filed under, so accepting a separately
// supplied date invited an entry headed with one date to be stored in another
// month's file — an LLM's notion of "today" comes from its prompt and can be
// stale or simply wrong near a midnight boundary.
//
// The em dash separator matches the format the episodic log already uses.
func composeEpisodicEntry(now time.Time, title string, content string) string {
	entry := fmt.Sprintf("## %s — %s\n\n%s", now.Format("2006-01-02"), title, content)

	// The month-creation path writes the entry directly rather than through
	// appendToBlockFile, so terminate the entry here to uphold the same
	// trailing-newline invariant on both paths.
	if !strings.HasSuffix(entry, "\n") {
		entry += "\n"
	}

	return entry
}

// HandleMemoryAppendEpisodic implements memory_append_episodic: append an
// entry to the current month's episodic log, creating that month's file
// (with bridge-generated frontmatter and heading) on its first append.
// Episodic files live in the blocks directory and share memory_append_block's
// serialized, never-branched append semantics.
func (b *Bridge) HandleMemoryAppendEpisodic(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	content, err := request.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	title, err := request.RequireString("title")
	if err != nil {
		return mcp.NewToolResultError("title is required"), nil
	}

	// A blank title would produce a heading that renders as a bare date with a
	// dangling separator, so treat it the same as omitting the argument.
	title = strings.TrimSpace(title)
	if title == "" {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInvalidContent,
			"title must not be empty", nil)), nil
	}

	// The bridge now owns the heading, so content carrying its own would
	// produce two headings for one entry. Rejecting it loudly is better than
	// silently accepting the duplicate: a caller still following the previous
	// convention finds out immediately rather than corrupting the log's
	// structure in a way that is only noticed when someone reads it back.
	if strings.HasPrefix(strings.TrimSpace(content), "#") {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInvalidContent,
			"content must not begin with a markdown heading; pass the heading text as the "+
				"title argument and put only the entry body in content", nil)), nil
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	now := time.Now()
	blockName := episodicBlockName(now)
	path := b.blockPath(blockName)

	entry := composeEpisodicEntry(now, title, content)

	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		// The current month's file already exists: append like any other
		// block. The leading newline opens a blank line between this entry
		// and the previous one; appendToBlockFile guarantees the existing
		// body already ends with a newline of its own.
		if appendErr := appendToBlockFile(path, "\n"+entry, b.Config.Memory.SummaryMaxLength); appendErr != nil {
			b.Logger.Error("memory_append_episodic: write failed", map[string]any{"handle": handle, "block": blockName, "error": appendErr.Error()})
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
		}

	case os.IsNotExist(statErr):
		// First append of the month: create the file with a bridge-generated
		// summary and heading, then include this entry as the initial body
		// content (Section 3.12). No leading newline here — the month heading
		// supplies its own blank line and nothing precedes this entry.
		heading := fmt.Sprintf("# %s %d\n\n", now.Month().String(), now.Year())
		fm := BlockFrontmatter{
			Summary:   fmt.Sprintf("Conversation log for %s %d", now.Month().String(), now.Year()),
			UpdatedAt: now.UTC(),
		}

		fileBytes, composeErr := composeBlockFile(fm, heading+entry)
		if composeErr != nil {
			b.Logger.Error("memory_append_episodic: compose failed", map[string]any{"handle": handle, "block": blockName, "error": composeErr.Error()})
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
		}

		if writeErr := atomicWriteFile(path, fileBytes, 0o644); writeErr != nil {
			b.Logger.Error("memory_append_episodic: create failed", map[string]any{"handle": handle, "block": blockName, "error": writeErr.Error()})
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
		}

	default:
		b.Logger.Error("memory_append_episodic: stat failed", map[string]any{"handle": handle, "block": blockName, "error": statErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	b.IndexCache.Invalidate()

	if sig, sigErr := computeSignature(path); sigErr == nil {
		b.Handles.SetBaseline(handle, blockName, sig)
	}
	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Info("memory_append_episodic: append", map[string]any{"handle": handle, "block": blockName, "bytes": len(entry)})

	response := OkResponse{Handle: handle, Ok: true}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

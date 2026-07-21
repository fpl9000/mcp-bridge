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
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// episodicBlockName returns the current month's episodic block name (e.g.
// "episodic-2026-06") from the system clock, per Section 3.12.
func episodicBlockName(now time.Time) string {
	return fmt.Sprintf("episodic-%04d-%02d", now.Year(), int(now.Month()))
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

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	now := time.Now()
	blockName := episodicBlockName(now)
	path := b.blockPath(blockName)

	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		// The current month's file already exists: append like any other
		// block.
		if appendErr := appendToBlockFile(path, content, b.Config.Memory.SummaryMaxLength); appendErr != nil {
			b.Logger.Error("memory_append_episodic: write failed", map[string]any{"handle": handle, "block": blockName, "error": appendErr.Error()})
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
		}

	case os.IsNotExist(statErr):
		// First append of the month: create the file with a bridge-generated
		// summary and heading, then include this entry as the initial body
		// content (Section 3.12).
		heading := fmt.Sprintf("# %s %d\n\n", now.Month().String(), now.Year())
		fm := BlockFrontmatter{
			Summary:   fmt.Sprintf("Conversation log for %s %d", now.Month().String(), now.Year()),
			UpdatedAt: now.UTC(),
		}

		fileBytes, composeErr := composeBlockFile(fm, heading+content)
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

	b.Logger.Info("memory_append_episodic: append", map[string]any{"handle": handle, "block": blockName, "bytes": len(content)})

	response := OkResponse{Handle: handle, Ok: true}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

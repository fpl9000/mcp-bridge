// memory_block.go implements the memory_get_block, memory_write_block, and
// memory_append_block tool handlers, per design spec Section 3.11. Block
// names are the only memory address the LLM ever supplies — the bridge maps
// a name to its on-disk file and manages YAML frontmatter transparently.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// blockNamePattern restricts block names to letters, digits, hyphens, and
// underscores (Section 4.8). Because "." is not in the allowed set, this
// pattern also rejects "..", "/", and "\" by construction — block names
// can never escape the blocks directory, and there is no separate path-
// traversal check to get wrong.
var blockNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// isValidBlockName reports whether name is an acceptable block name.
func isValidBlockName(name string) bool {
	return name != "" && blockNamePattern.MatchString(name)
}

// GetBlockResponse is the success shape returned by memory_get_block.
type GetBlockResponse struct {
	Handle               string `json:"handle"`
	Content              string `json:"content"`
	ChangedSinceLastRead bool   `json:"changed_since_last_read"`
}

// blockPath returns the on-disk path for a block name. The LLM never sees
// this value — it addresses blocks by name only (Section 3.11).
func (b *Bridge) blockPath(name string) string {
	return filepath.Join(b.Config.BlocksDirectory(), name+".md")
}

// appendToBlockFile reads the block file at path, appends text to its body
// exactly as given (Section 3.11: "the text is appended exactly as
// provided"), refreshes updated_at, and writes the result back atomically.
// The existing summary is preserved unchanged. The caller must already hold
// the memory mutex and must already have confirmed the file exists.
func appendToBlockFile(path string, text string, summaryMaxLength int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	fm, body, hasFrontmatter := splitFrontmatter(raw)
	if !hasFrontmatter {
		// The file was hand-edited without preserving frontmatter; derive a
		// placeholder summary so the frontmatter this append writes back is
		// well-formed (Section 3.16's tolerance for damaged frontmatter).
		fm.Summary = deriveDefaultSummary(body, summaryMaxLength)
	}

	fm.UpdatedAt = time.Now().UTC()

	// Guarantee the appended text begins at the start of a line. Without this,
	// an append whose first line is a markdown heading gets welded onto the
	// final line of the previous entry, where a "##" no longer at column zero
	// stops being a heading at all. The caller cannot reliably prevent this on
	// its own: deciding whether a leading newline is needed requires knowing
	// the block's current trailing bytes, which a caller that only appends has
	// never read. Doing it here also repairs blocks already stored without a
	// trailing newline.
	//
	// A caller that supplies its own leading newline already satisfies the
	// guarantee, so no second newline is inserted; that would turn every such
	// append into a gratuitous blank line. Note the consequence: text can no
	// longer be appended mid-line to continue the block's last line.
	if len(body) > 0 && !strings.HasSuffix(body, "\n") && !strings.HasPrefix(text, "\n") {
		body += "\n"
	}

	combined := body + text

	// Re-establish a trailing newline on the way out, so the next append to
	// this block starts from a well-formed state regardless of whether the
	// text just appended happened to end with one.
	if !strings.HasSuffix(combined, "\n") {
		combined += "\n"
	}

	fileBytes, err := composeBlockFile(fm, combined)
	if err != nil {
		return err
	}

	return atomicWriteFile(path, fileBytes, 0o644)
}

// HandleMemoryGetBlock implements memory_get_block: return the block's body
// with frontmatter stripped, set the read baseline, and report
// changed_since_last_read.
func (b *Bridge) HandleMemoryGetBlock(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	blockName, err := request.RequireString("block_name")
	if err != nil {
		return mcp.NewToolResultError("block_name is required"), nil
	}

	if !isValidBlockName(blockName) {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInvalidBlockName,
			fmt.Sprintf("Block name %q contains invalid characters; use letters, digits, hyphens, and underscores", blockName), nil)), nil
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	path := b.blockPath(blockName)
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeBlockNotFound,
				fmt.Sprintf("Block %q does not exist for this handle", blockName), nil)), nil
		}

		b.Logger.Error("memory_get_block: read failed", map[string]any{"handle": handle, "block": blockName, "error": readErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	_, body, _ := splitFrontmatter(raw)

	sig, statErr := computeSignature(path)
	if statErr != nil {
		b.Logger.Error("memory_get_block: stat failed", map[string]any{"handle": handle, "block": blockName, "error": statErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	baseline, hadBaseline := b.Handles.Baseline(handle, blockName)
	changed := hadBaseline && !baseline.Equal(sig)

	b.Handles.SetBaseline(handle, blockName, sig)
	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Info("memory_get_block: read", map[string]any{
		"handle": handle, "block": blockName, "bytes": len(body), "changed_since_last_read": changed,
	})

	response := GetBlockResponse{Handle: handle, Content: body, ChangedSinceLastRead: changed}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

// HandleMemoryWriteBlock implements memory_write_block: replace a block's
// body, creating it if absent (summary required for creation), managing the
// summary/updated_at frontmatter. Writes are unconditional last-writer-wins
// in this build.
func (b *Bridge) HandleMemoryWriteBlock(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	blockName, err := request.RequireString("block_name")
	if err != nil {
		return mcp.NewToolResultError("block_name is required"), nil
	}

	if !isValidBlockName(blockName) {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInvalidBlockName,
			fmt.Sprintf("Block name %q contains invalid characters; use letters, digits, hyphens, and underscores", blockName), nil)), nil
	}

	content, err := request.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	// mcp-go has no way to distinguish "summary omitted" from "summary is
	// the empty string" via GetString's default-value parameter, but the
	// summary contract (Section 3.11) requires exactly that distinction —
	// an omitted summary preserves the existing one, while an explicit empty
	// string replaces it. Reading the raw arguments map lets us tell them
	// apart.
	summaryProvided := false
	summaryValue := ""
	if raw, ok := request.GetArguments()["summary"]; ok {
		if s, ok := raw.(string); ok {
			summaryProvided = true
			summaryValue = s
		}
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	path := b.blockPath(blockName)
	existingRaw, statErr := os.ReadFile(path)
	blockExists := statErr == nil

	var finalSummary string
	if blockExists {
		existingFM, _, _ := splitFrontmatter(existingRaw)
		finalSummary = existingFM.Summary
		if summaryProvided {
			finalSummary = summaryValue
		}
	} else {
		if !summaryProvided {
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeSummaryRequired,
				"Summary is required when creating a new block", nil)), nil
		}
		finalSummary = summaryValue
	}

	if len(finalSummary) > b.Config.Memory.SummaryMaxLength {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeSummaryTooLong,
			fmt.Sprintf("Summary exceeds the maximum length of %d characters", b.Config.Memory.SummaryMaxLength), nil)), nil
	}

	fm := BlockFrontmatter{Summary: finalSummary, UpdatedAt: time.Now().UTC()}
	fileBytes, composeErr := composeBlockFile(fm, content)
	if composeErr != nil {
		b.Logger.Error("memory_write_block: compose failed", map[string]any{"handle": handle, "block": blockName, "error": composeErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	if writeErr := atomicWriteFile(path, fileBytes, 0o644); writeErr != nil {
		b.Logger.Error("memory_write_block: write failed", map[string]any{"handle": handle, "block": blockName, "error": writeErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	if sig, sigErr := computeSignature(path); sigErr == nil {
		b.Handles.SetBaseline(handle, blockName, sig)
	}
	b.Handles.Touch(handle)
	b.IndexCache.Invalidate()
	b.Persist.MarkDirty()

	b.Logger.Info("memory_write_block: write", map[string]any{"handle": handle, "block": blockName, "bytes": len(content)})

	response := OkResponse{Handle: handle, Ok: true}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

// HandleMemoryAppendBlock implements memory_append_block: append text to an
// existing block. Appends never create a block (Section 3.11) — a missing
// block is BLOCK_NOT_FOUND, directing the LLM to memory_write_block instead.
func (b *Bridge) HandleMemoryAppendBlock(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	blockName, err := request.RequireString("block_name")
	if err != nil {
		return mcp.NewToolResultError("block_name is required"), nil
	}

	if !isValidBlockName(blockName) {
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInvalidBlockName,
			fmt.Sprintf("Block name %q contains invalid characters; use letters, digits, hyphens, and underscores", blockName), nil)), nil
	}

	text, err := request.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	path := b.blockPath(blockName)
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeBlockNotFound,
				fmt.Sprintf("Block %q does not exist for this handle", blockName), nil)), nil
		}

		b.Logger.Error("memory_append_block: stat failed", map[string]any{"handle": handle, "block": blockName, "error": statErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	// An empty append is a no-op: no write, no updated_at bump (Section
	// 8.1.4's "Empty text" test case).
	if text != "" {
		if appendErr := appendToBlockFile(path, text, b.Config.Memory.SummaryMaxLength); appendErr != nil {
			b.Logger.Error("memory_append_block: write failed", map[string]any{"handle": handle, "block": blockName, "error": appendErr.Error()})
			return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
		}
		b.IndexCache.Invalidate()
	}

	// The read baseline is refreshed to the post-append signature even on a
	// no-op append, so this handle's next write of the block isn't
	// mistakenly treated as racing against its own append (Section 3.11).
	if sig, sigErr := computeSignature(path); sigErr == nil {
		b.Handles.SetBaseline(handle, blockName, sig)
	}
	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Info("memory_append_block: append", map[string]any{"handle": handle, "block": blockName, "bytes": len(text)})

	response := OkResponse{Handle: handle, Ok: true}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

// memory_core.go implements the memory_get_core and memory_write_core tool
// handlers, per design spec Section 3.9. Core (core.md) has no YAML
// frontmatter — it is always loaded in full and does not appear in the
// derived index — so these handlers never touch the frontmatter machinery
// that memory_block.go uses.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
)

// GetCoreResponse is the success shape returned by memory_get_core.
type GetCoreResponse struct {
	Handle               string `json:"handle"`
	Content              string `json:"content"`
	ChangedSinceLastRead bool   `json:"changed_since_last_read"`
}

// OkResponse is the shared success shape for every memory tool whose only
// job is to confirm a write succeeded (memory_write_core, memory_write_block,
// memory_append_block, memory_append_episodic).
type OkResponse struct {
	Handle string `json:"handle"`
	Ok     bool   `json:"ok"`
}

// readCoreContent reads core.md's raw bytes verbatim. A missing file is
// cold-start-normal (Section 10 of IMPLEMENTATION-PROMPT-minimal.md): the bridge
// never seeds core.md itself, so a brand-new memory store has none yet.
func (b *Bridge) readCoreContent() (content string, sig Signature, err error) {
	corePath := b.Config.CorePath()

	raw, readErr := os.ReadFile(corePath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", Signature{}, nil
		}
		return "", Signature{}, readErr
	}

	fileSig, statErr := computeSignature(corePath)
	if statErr != nil {
		return "", Signature{}, statErr
	}

	return string(raw), fileSig, nil
}

// HandleMemoryGetCore implements memory_get_core: return core content, set
// this handle's read baseline, and report changed_since_last_read (Section
// 3.9's shared read handler pseudo-code, specialized for core).
func (b *Bridge) HandleMemoryGetCore(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	content, sig, readErr := b.readCoreContent()
	if readErr != nil {
		b.Logger.Error("memory_get_core: read failed", map[string]any{"handle": handle, "error": readErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	baseline, hadBaseline := b.Handles.Baseline(handle, CoreTargetName)
	changed := hadBaseline && !baseline.Equal(sig)

	b.Handles.SetBaseline(handle, CoreTargetName, sig)
	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Info("memory_get_core: read", map[string]any{
		"handle":                  handle,
		"bytes":                   len(content),
		"changed_since_last_read": changed,
	})

	response := GetCoreResponse{Handle: handle, Content: content, ChangedSinceLastRead: changed}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

// HandleMemoryWriteCore implements memory_write_core: replace core.md
// atomically with no frontmatter. Writes are unconditional last-writer-wins
// in this build (IMPLEMENTATION-PROMPT-minimal.md Section 2) — there is no baseline
// comparison or branch routing on the write path.
func (b *Bridge) HandleMemoryWriteCore(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	if writeErr := atomicWriteFile(b.Config.CorePath(), []byte(content), 0o644); writeErr != nil {
		b.Logger.Error("memory_write_core: write failed", map[string]any{"handle": handle, "error": writeErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	if sig, statErr := computeSignature(b.Config.CorePath()); statErr == nil {
		b.Handles.SetBaseline(handle, CoreTargetName, sig)
	}
	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Info("memory_write_core: write", map[string]any{"handle": handle, "bytes": len(content)})

	response := OkResponse{Handle: handle, Ok: true}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

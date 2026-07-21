// memory_index.go implements the memory_get_index tool handler: the derived
// index is assembled on demand from block frontmatter (there is no stored
// index file), per design spec Section 3.10. A single process-wide cache
// (rather than the full design's per-handle cache) is sufficient here
// because branching is omitted from this build (IMPLEMENTATION-PROMPT.md
// Section 2) — with no per-handle branches, every handle sees the same base
// files, so there is no per-handle divergence for the cache to track.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// indexSchemaVersion is the schema_version field of every assembled index,
// per Section 3.10's index schema.
const indexSchemaVersion = 1

// IndexBlockEntry is one row of the derived index.
type IndexBlockEntry struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"`
}

// Index is the full derived-index payload.
type Index struct {
	SchemaVersion int               `json:"schema_version"`
	Blocks        []IndexBlockEntry `json:"blocks"`
}

// GetIndexResponse is the success shape returned by memory_get_index.
type GetIndexResponse struct {
	Handle string `json:"handle"`
	Index  Index  `json:"index"`
}

// IndexCache holds the most recently assembled index so repeated
// memory_get_index / memory_start_conversation calls avoid re-walking the
// blocks directory. It is invalidated explicitly by any bridge-mediated
// block write (memory_write_block, memory_append_block,
// memory_append_episodic) and also self-invalidates whenever the blocks
// directory's own mtime has moved on — the fallback that catches a block
// file the user created or edited directly outside the bridge (Section
// 3.10: "invalidated when that handle writes a block or when the blocks
// directory's most recent mtime changes").
type IndexCache struct {
	mu         sync.Mutex
	valid      bool
	index      Index
	dirModTime time.Time
}

// NewIndexCache returns an empty, invalid cache — the first Get call always
// assembles the index from disk.
func NewIndexCache() *IndexCache {
	return &IndexCache{}
}

// Invalidate discards the cached index so the next Get call reassembles it
// from disk. Callers must hold the memory mutex when invalidating after a
// write, so the invalidation is ordered correctly with respect to concurrent
// reads (Section 3.10's "invalidated when that handle writes a block").
func (c *IndexCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
}

// Get returns the cached index, assembling it from disk on a cache miss —
// either an explicit Invalidate() or a change in the blocks directory's own
// mtime since the value was cached.
func (c *IndexCache) Get(blocksDir string, summaryMaxLength int) (Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var currentDirModTime time.Time
	if info, statErr := os.Stat(blocksDir); statErr == nil {
		currentDirModTime = info.ModTime()
	}

	if c.valid && currentDirModTime.Equal(c.dirModTime) {
		return c.index, nil
	}

	index, err := assembleIndex(blocksDir, summaryMaxLength)
	if err != nil {
		return Index{}, err
	}

	c.index = index
	c.valid = true
	c.dirModTime = currentDirModTime
	return index, nil
}

// assembleIndex walks blocksDir and builds one index entry per block file.
// Files with missing or damaged frontmatter are tolerated (Section 3.16):
// they get a mechanically derived placeholder summary rather than causing an
// error. The bridge-private state file lives in the memory root, not the
// blocks directory, so it is naturally excluded without special-casing.
func assembleIndex(blocksDir string, summaryMaxLength int) (Index, error) {
	entries, err := os.ReadDir(blocksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Index{SchemaVersion: indexSchemaVersion, Blocks: []IndexBlockEntry{}}, nil
		}
		return Index{}, err
	}

	blocks := make([]IndexBlockEntry, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		// Orphaned temp files from an interrupted write are never valid
		// block content; the startup sweeper normally removes them, but
		// skip them here too in case one is still mid-write.
		if strings.Contains(name, ".tmp.") {
			continue
		}

		blockPath := filepath.Join(blocksDir, name)

		raw, readErr := os.ReadFile(blockPath)
		if readErr != nil {
			// The file vanished between ReadDir and ReadFile (e.g. a
			// concurrent process removed it); skip it rather than fail the
			// entire index assembly.
			continue
		}

		fm, body, hasFrontmatter := splitFrontmatter(raw)

		summary := fm.Summary
		updatedAt := fm.UpdatedAt

		if !hasFrontmatter {
			summary = deriveDefaultSummary(body, summaryMaxLength)
			if info, statErr := os.Stat(blockPath); statErr == nil {
				updatedAt = info.ModTime()
			}
		}

		blocks = append(blocks, IndexBlockEntry{
			Name:      strings.TrimSuffix(name, ".md"),
			Summary:   summary,
			UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		})
	}

	// Stable sort by name (Section 3.10: "Insertion order would be subtle to
	// maintain across bridge restarts").
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Name < blocks[j].Name })

	return Index{SchemaVersion: indexSchemaVersion, Blocks: blocks}, nil
}

// HandleMemoryGetIndex implements memory_get_index.
func (b *Bridge) HandleMemoryGetIndex(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle, err := request.RequireString("handle")
	if err != nil {
		return mcp.NewToolResultError("handle is required"), nil
	}

	if verr := b.Handles.Validate(handle); verr != nil {
		return mcp.NewToolResultText(newErrorResponse("", verr.Code, verr.Message, nil)), nil
	}

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	index, indexErr := b.IndexCache.Get(b.Config.BlocksDirectory(), b.Config.Memory.SummaryMaxLength)
	if indexErr != nil {
		b.Logger.Error("memory_get_index: assembly failed", map[string]any{"handle": handle, "error": indexErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	b.Logger.Debug("memory_get_index: assembled", map[string]any{"handle": handle, "block_count": len(index.Blocks)})

	response := GetIndexResponse{Handle: handle, Index: index}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

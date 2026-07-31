// memory_index.go implements the memory_get_index tool handler: the derived
// index is assembled on demand from block frontmatter (there is no stored
// index file), per design spec Section 3.10. A single process-wide cache
// (rather than the full design's per-handle cache) is sufficient here
// because branching is omitted from this build (IMPLEMENTATION-PROMPT-minimal.md
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
// directory's observable state has moved on — the fallback that catches a
// block file the user created, removed, or edited directly outside the
// bridge (Section 3.10: "invalidated when that handle writes a block or when
// the blocks directory's most recent mtime changes").
//
// Section 3.10's "most recent mtime" is read here as the most recent mtime
// *among the block files*, not the mtime of the directory inode. The two are
// not equivalent, and the difference is the whole point: a directory's mtime
// tracks changes to its set of entries, so it moves when a file is created,
// renamed, or removed but not when an existing file's contents are rewritten
// through its existing entry. An editor that saves in place — Emacs with
// `backup-by-copying` set to t, for instance — produces exactly that
// undetectable case, and a directory-keyed cache would go on serving the
// pre-edit index indefinitely.
type IndexCache struct {
	mu          sync.Mutex
	valid       bool
	index       Index
	fingerprint blocksFingerprint
}

// blocksFingerprint is a cheap summary of everything about the blocks
// directory that can affect the assembled index, used to decide whether a
// cached index is still current.
//
// Computing it costs one directory read plus a stat per block file, which is
// far less than the read-and-parse of every file that assembling the index
// actually requires, so the cache still earns its place.
type blocksFingerprint struct {
	// fileCount guards the case where one file is removed and another added
	// closely enough in time that the maximum mtime is unchanged.
	fileCount int

	// maxModTime is the most recent mtime among the block files, which is
	// what catches an in-place rewrite of an existing file.
	maxModTime time.Time

	// totalSize guards against an edit landing inside the filesystem's
	// timestamp granularity, which on a coarse filesystem can be a second or
	// more. It mirrors the ModTime-plus-size pairing that handles.go already
	// uses for per-file change detection.
	totalSize int64
}

// Equal reports whether two fingerprints describe the same directory state.
// It cannot be written as "==" because time.Time carries a monotonic reading
// and a Location pointer that may differ between two values representing the
// same instant, so time.Time.Equal is required.
func (f blocksFingerprint) Equal(other blocksFingerprint) bool {
	return f.fileCount == other.fileCount &&
		f.maxModTime.Equal(other.maxModTime) &&
		f.totalSize == other.totalSize
}

// computeBlocksFingerprint summarizes the current state of the blocks
// directory. A directory that cannot be read yields the zero fingerprint,
// which simply means the cache treats the state as unchanged from any other
// unreadable moment; assembleIndex reports the real error on that path.
func computeBlocksFingerprint(blocksDir string) blocksFingerprint {
	entries, err := os.ReadDir(blocksDir)
	if err != nil {
		return blocksFingerprint{}
	}

	var fingerprint blocksFingerprint

	for _, entry := range entries {
		// Fingerprint exactly the set of files that assembleIndex turns into
		// index rows. Including anything else would invalidate the cache for
		// changes that cannot alter the index — an editor rewriting its
		// backup file on every save, for example.
		if entry.IsDir() || !isBlockFile(entry.Name()) {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			// The file vanished between ReadDir and Info. Skipping it here
			// only risks a redundant reassembly, never a stale index, since
			// its disappearance changes the count.
			continue
		}

		fingerprint.fileCount++
		fingerprint.totalSize += info.Size()

		if info.ModTime().After(fingerprint.maxModTime) {
			fingerprint.maxModTime = info.ModTime()
		}
	}

	return fingerprint
}

// isBlockFile reports whether a directory entry name is one that assembleIndex
// will turn into an index row. It exists so that the index and the cache
// fingerprint cannot drift apart on which files they consider: a file counted
// by one but ignored by the other would reintroduce staleness.
func isBlockFile(name string) bool {
	if !strings.HasSuffix(name, ".md") {
		return false
	}

	// Orphaned temp files from an interrupted write are never valid block
	// content; the startup sweeper normally removes them, but skip them here
	// too in case one is still mid-write.
	return !strings.Contains(name, ".tmp.")
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
// either an explicit Invalidate() or a change in the blocks directory's
// fingerprint since the value was cached.
func (c *IndexCache) Get(blocksDir string, summaryMaxLength int) (Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	currentFingerprint := computeBlocksFingerprint(blocksDir)

	if c.valid && currentFingerprint.Equal(c.fingerprint) {
		return c.index, nil
	}

	index, err := assembleIndex(blocksDir, summaryMaxLength)
	if err != nil {
		return Index{}, err
	}

	c.index = index
	c.valid = true
	c.fingerprint = currentFingerprint
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
		if !isBlockFile(name) {
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

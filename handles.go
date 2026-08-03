// handles.go implements handle minting, the handle map, and per-handle read
// baselines, per design spec Section 3.14. This build omits branching (see
// IMPLEMENTATION-PROMPT-minimal.md Section 2), so HandleState carries no branch map —
// only baselines and last-activity survive from the full design's
// HandleState. Handle eviction runs only during the deferred maintenance
// sweep, so handles minted in this build are tracked but never evicted; that
// is expected, not a bug.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// CoreTargetName is the baseline map key used for core.md. Blocks use their
// own block name as the key, so this constant exists only to give core a
// name that can never collide with a real block name (block names are
// restricted to letters, digits, hyphens, and underscores by
// isValidBlockName in memory_block.go — "core" happens to be a legal block
// name syntactically, but the bridge never creates a block by that name, and
// core and blocks live in separate namespaces here regardless).
const CoreTargetName = "core"

// handleAlphabet is the character set from which handles are minted:
// lowercase letters and digits, per Section 3.14's "8 lowercase alphanumeric
// characters" format.
const handleAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// Signature is the cheap content-change detector used for the
// changed_since_last_read flag: ModTime plus size. Section 3.14 notes that a
// content hash is a possible v1.x upgrade; ModTime+size is sufficient here.
type Signature struct {
	ModTime time.Time
	Size    int64
}

// Equal compares two signatures for the purpose of change detection. This is
// deliberately not "==": time.Time carries a monotonic reading and a
// Location pointer that can differ between two Signatures representing the
// same instant (e.g., one freshly stat'd, one round-tripped through JSON
// persistence), so time.Time.Equal is required for a correct comparison.
func (s Signature) Equal(other Signature) bool {
	return s.ModTime.Equal(other.ModTime) && s.Size == other.Size
}

// HandleState is the per-handle state the bridge tracks. The full design's
// Branches map is omitted — see the package comment above.
type HandleState struct {
	Baselines    map[string]Signature
	LastActivity time.Time
}

// HandleValidationError is returned by HandleMap.Validate and carries the
// stable error code alongside a message safe to return to the LLM.
type HandleValidationError struct {
	Code    string
	Message string
}

func (e *HandleValidationError) Error() string {
	return e.Message
}

// HandleMap is the bridge's in-memory registry of live handles. All access
// is serialized by its own mutex, independent of the memory mutex — handle
// bookkeeping and memory file I/O are separate concerns (a read handler
// validates the handle, then separately acquires the memory mutex).
type HandleMap struct {
	mu       sync.Mutex
	handles  map[string]*HandleState
	rng      *rand.Rand
	idLength int
}

// NewHandleMap creates an empty handle map that mints handles of the given
// length. The PRNG is seeded once at construction (bridge startup), per
// Section 3.14's "standard PRNG (math/rand, seeded at bridge startup)".
func NewHandleMap(idLength int) *HandleMap {
	return &HandleMap{
		handles:  make(map[string]*HandleState),
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		idLength: idLength,
	}
}

// handleFormatPattern matches a well-formed handle of any length; the actual
// required length is checked separately against the configured id_length,
// since the compiled regexp is built once but id_length is configurable.
var handleFormatPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// isWellFormed reports whether handle matches the required length and
// character set, without consulting the handle map.
func (hm *HandleMap) isWellFormed(handle string) bool {
	return len(handle) == hm.idLength && handleFormatPattern.MatchString(handle)
}

// generateCandidate produces one random candidate handle. Callers must hold
// hm.mu.
func (hm *HandleMap) generateCandidate() string {
	buf := make([]byte, hm.idLength)
	for i := range buf {
		buf[i] = handleAlphabet[hm.rng.Intn(len(handleAlphabet))]
	}
	return string(buf)
}

// Mint allocates a fresh handle, registers it with empty baselines, and
// returns it. Collisions are checked against the in-memory map and retried;
// per Section 3.8 this is "vanishingly unlikely... but cheap to verify."
func (hm *HandleMap) Mint() string {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	for {
		candidate := hm.generateCandidate()
		if _, exists := hm.handles[candidate]; exists {
			continue
		}

		hm.handles[candidate] = &HandleState{
			Baselines:    make(map[string]Signature),
			LastActivity: time.Now(),
		}
		return candidate
	}
}

// Restore registers a handle recovered from persisted state at startup,
// preserving its baselines and last-activity time exactly as loaded.
func (hm *HandleMap) Restore(handle string, baselines map[string]Signature, lastActivity time.Time) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if baselines == nil {
		baselines = make(map[string]Signature)
	}

	hm.handles[handle] = &HandleState{
		Baselines:    baselines,
		LastActivity: lastActivity,
	}
}

// Validate checks a handle presented by the LLM. A malformed handle (wrong
// length or character set) is rejected immediately; a well-formed handle not
// present in the map is rejected as unrecognized. Per Section 3.14, this
// build has no lazy-adoption backstop to fall back on: without branching,
// there are no branch files to scan, so an unrecognized well-formed handle
// simply means the caller must re-initialize.
func (hm *HandleMap) Validate(handle string) *HandleValidationError {
	if !hm.isWellFormed(handle) {
		return &HandleValidationError{
			Code:    ErrCodeMalformedHandle,
			Message: fmt.Sprintf("Handle %q is malformed. Call memory_start_conversation to obtain a fresh handle and retry.", handle),
		}
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	if _, exists := hm.handles[handle]; !exists {
		return &HandleValidationError{
			Code:    ErrCodeInvalidHandle,
			Message: fmt.Sprintf("Handle %q is not recognized. Call memory_start_conversation to obtain a fresh handle and retry.", handle),
		}
	}

	return nil
}

// Touch updates a handle's last-activity time. Called on every successful
// memory tool call so the retention-based eviction policy (deferred to
// maintenance) has an accurate idle clock once implemented.
func (hm *HandleMap) Touch(handle string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if state, exists := hm.handles[handle]; exists {
		state.LastActivity = time.Now()
	}
}

// Baseline returns the recorded signature for (handle, target) and whether
// one exists. A missing baseline means this handle has never read target
// before.
func (hm *HandleMap) Baseline(handle string, target string) (Signature, bool) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	state, exists := hm.handles[handle]
	if !exists {
		return Signature{}, false
	}

	sig, exists := state.Baselines[target]
	return sig, exists
}

// SetBaseline records the signature of the content most recently returned to
// handle for target. Recorded on every read (Section 3.9's read handler
// pseudo-code, step 6).
func (hm *HandleMap) SetBaseline(handle string, target string, sig Signature) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if state, exists := hm.handles[handle]; exists {
		state.Baselines[target] = sig
	}
}

// Snapshot returns a deep copy of every live handle's state, for use by the
// persistence layer when composing a checkpoint. A deep copy is necessary
// because the caller serializes it to JSON outside the handle map's lock.
func (hm *HandleMap) Snapshot() map[string]*HandleState {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	snapshot := make(map[string]*HandleState, len(hm.handles))
	for handle, state := range hm.handles {
		baselinesCopy := make(map[string]Signature, len(state.Baselines))
		for target, sig := range state.Baselines {
			baselinesCopy[target] = sig
		}

		snapshot[handle] = &HandleState{
			Baselines:    baselinesCopy,
			LastActivity: state.LastActivity,
		}
	}

	return snapshot
}

// StartConversationResponse is the success shape returned by
// memory_start_conversation. Per Section 3 of IMPLEMENTATION-PROMPT-minimal.md, this
// build's response bundles core and the index into the same round trip
// (rather than the full design's handle-only response), so a fresh
// conversation never needs a second call before it has both.
type StartConversationResponse struct {
	Handle string `json:"handle"`
	Core   string `json:"core"`
	Index  Index  `json:"index"`

	// AlwaysLoaded carries the blocks named by memory.always_load_blocks.
	// Omitted from the JSON when empty so that an installation not using the
	// feature -- including every fresh one -- sees exactly the previous
	// response shape.
	AlwaysLoaded []AlwaysLoadedBlock `json:"always_loaded,omitempty"`
}

// AlwaysLoadedBlock is one entry of StartConversationResponse.AlwaysLoaded.
//
// Content is the block body with frontmatter stripped, matching what
// memory_get_block returns, so the caller sees a block identically however it
// arrived. Skipped is set instead of Content when the block could not be
// loaded, with Reason saying why; the two are mutually exclusive.
type AlwaysLoadedBlock struct {
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// loadAlwaysLoadBlocks reads the configured always-load blocks, in configured
// order, and records a read baseline for each under this handle.
//
// A missing block is reported as skipped rather than treated as an error. A
// fresh installation has no blocks at all, and an operator may name a block
// intending to create it later; failing conversation startup over that would
// make the feature hazardous to configure. The skip is visible in the response
// and logged, so it does not pass unnoticed either.
//
// The baseline matters: without it, a subsequent memory_get_block on the same
// block would report changed_since_last_read as false on its first call
// (no baseline recorded) even though the caller already holds the content --
// and would then miss a genuine external edit made in between. Recording the
// baseline here makes an always-loaded block behave exactly as if the caller
// had read it explicitly.
//
// The caller must already hold the memory mutex.
func (b *Bridge) loadAlwaysLoadBlocks(handle string) []AlwaysLoadedBlock {
	names := b.Config.Memory.AlwaysLoadBlocks
	if len(names) == 0 {
		return nil
	}

	maxBytes := b.Config.Memory.AlwaysLoadMaxBytes
	loaded := make([]AlwaysLoadedBlock, 0, len(names))
	seen := make(map[string]bool, len(names))
	total := 0

	for _, name := range names {
		// A duplicate would be paid for twice in the context window and add
		// nothing, so drop it silently -- it is a harmless config redundancy
		// rather than a mistake worth surfacing on every conversation.
		if seen[name] {
			continue
		}
		seen[name] = true

		path := b.blockPath(name)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			reason := "block does not exist"
			if !os.IsNotExist(readErr) {
				reason = "block could not be read"
				b.Logger.Error("memory_start_conversation: always-load block read failed",
					map[string]any{"handle": handle, "block": name, "error": readErr.Error()})
			} else {
				b.Logger.Info("memory_start_conversation: always-load block absent",
					map[string]any{"handle": handle, "block": name})
			}

			loaded = append(loaded, AlwaysLoadedBlock{Name: name, Skipped: true, Reason: reason})
			continue
		}

		_, body, _ := splitFrontmatter(raw)

		// Enforce the cap before adding, so the reported total never exceeds
		// it. Later blocks are still considered: a small one after a large one
		// may still fit, and skipping it would make the outcome depend on
		// ordering more than necessary.
		if maxBytes > 0 && total+len(body) > maxBytes {
			b.Logger.Info("memory_start_conversation: always-load block exceeds size cap",
				map[string]any{"handle": handle, "block": name, "bytes": len(body), "total": total, "cap": maxBytes})
			loaded = append(loaded, AlwaysLoadedBlock{
				Name:    name,
				Skipped: true,
				Reason:  "would exceed memory.always_load_max_bytes",
			})
			continue
		}

		total += len(body)

		if sig, statErr := computeSignature(path); statErr == nil {
			b.Handles.SetBaseline(handle, name, sig)
		}

		loaded = append(loaded, AlwaysLoadedBlock{Name: name, Content: body})
	}

	b.Logger.Info("memory_start_conversation: always-load blocks resolved",
		map[string]any{"handle": handle, "requested": len(names), "bytes": total})

	return loaded
}

// HandleMemoryStartConversation implements memory_start_conversation: mint a
// fresh handle, then read core and derive the index under that handle so
// this handle's baselines are recorded exactly as if it had called
// memory_get_core and memory_get_index separately (Section 3.8).
func (b *Bridge) HandleMemoryStartConversation(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	handle := b.Handles.Mint()
	b.Persist.MarkDirty()

	b.Logger.Info("memory_start_conversation: handle minted", map[string]any{"handle": handle})

	b.Mutex.Lock()
	defer b.Mutex.Unlock()

	content, sig, readErr := b.readCoreContent()
	if readErr != nil {
		b.Logger.Error("memory_start_conversation: core read failed", map[string]any{"handle": handle, "error": readErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}
	b.Handles.SetBaseline(handle, CoreTargetName, sig)

	index, indexErr := b.IndexCache.Get(b.Config.BlocksDirectory(), b.Config.Memory.SummaryMaxLength)
	if indexErr != nil {
		b.Logger.Error("memory_start_conversation: index assembly failed", map[string]any{"handle": handle, "error": indexErr.Error()})
		return mcp.NewToolResultText(newErrorResponse(handle, ErrCodeInternalError, internalErrorMessage, nil)), nil
	}

	alwaysLoaded := b.loadAlwaysLoadBlocks(handle)

	b.Handles.Touch(handle)
	b.Persist.MarkDirty()

	response := StartConversationResponse{Handle: handle, Core: content, Index: index, AlwaysLoaded: alwaysLoaded}
	encoded, _ := json.Marshal(response)
	return mcp.NewToolResultText(string(encoded)), nil
}

// computeSignature reads a file's ModTime and size to build its current
// Signature. Returns an error if the file does not exist or cannot be
// stat'd; callers treat a missing file as "no signature" (e.g. the target
// doesn't exist yet).
func computeSignature(path string) (Signature, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Signature{}, err
	}

	return Signature{ModTime: info.ModTime(), Size: info.Size()}, nil
}

// persistence.go implements .bridge-state.json load, checkpoint, and
// shutdown write, per design spec Section 3.18. This build omits branching
// (see IMPLEMENTATION-PROMPT-minimal.md Section 2), so the persisted state carries no
// branch maps — only live handles, their read baselines, and last-activity
// times. Because there are no branch files on disk in this build, the
// filesystem-reconciliation and lazy-adoption steps of the full design
// (verifying branch-map entries still exist, scanning for orphaned branch
// files) do not apply; "load and reconcile" here is simply "load."
package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// PersistedSignature is the JSON-serializable form of Signature.
type PersistedSignature struct {
	ModTime time.Time `json:"mod_time"`
	Size    int64     `json:"size"`
}

// PersistedHandleState is the JSON-serializable form of HandleState.
type PersistedHandleState struct {
	Baselines    map[string]PersistedSignature `json:"baselines"`
	LastActivity time.Time                     `json:"last_activity"`
}

// PersistedState is the top-level shape of .bridge-state.json.
type PersistedState struct {
	Handles map[string]PersistedHandleState `json:"handles"`
}

// Persistence owns the debounced-checkpoint state file for a single
// HandleMap. All exported methods are safe for concurrent use.
type Persistence struct {
	mu       sync.Mutex
	path     string
	interval time.Duration
	handles  *HandleMap
	logger   *Logger
	dirty    bool
	timer    *time.Timer
}

// loadPersistedState reads and parses the state file at path. A missing file
// is normal (first run) and yields an empty state. A present-but-unparseable
// file logs a warning and falls back to an empty state, per Section 3.18's
// corruption handling: "the bridge is never worse off than a
// lazy-adoption-only design, even in the corruption case" — in this
// branchless build, "worse off" simply means starting with no recovered
// handles, which is safe (the LLM re-initializes on its next call).
func loadPersistedState(path string, logger *Logger) *PersistedState {
	empty := &PersistedState{Handles: map[string]PersistedHandleState{}}

	raw, err := os.ReadFile(path)
	if err != nil {
		return empty
	}

	var state PersistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		logger.Warn("state file corrupt at startup; starting clean", map[string]any{
			"path":  path,
			"error": err.Error(),
		})
		return empty
	}

	if state.Handles == nil {
		state.Handles = map[string]PersistedHandleState{}
	}

	return &state
}

// NewPersistence loads any existing state file at cfg.StateFilePath(),
// restores every recovered handle into handleMap, and returns a Persistence
// ready to accept dirty markers. recoveredCount is reported so main.go can
// log it at startup per Section 3.22's "handles recovered" field.
func NewPersistence(cfg *Config, handleMap *HandleMap, logger *Logger) (p *Persistence, recoveredCount int) {
	state := loadPersistedState(cfg.StateFilePath(), logger)

	for handle, persistedState := range state.Handles {
		baselines := make(map[string]Signature, len(persistedState.Baselines))
		for target, sig := range persistedState.Baselines {
			baselines[target] = Signature{ModTime: sig.ModTime, Size: sig.Size}
		}

		handleMap.Restore(handle, baselines, persistedState.LastActivity)
		recoveredCount++
	}

	p = &Persistence{
		path:     cfg.StateFilePath(),
		interval: time.Duration(cfg.Persistence.CheckpointIntervalSeconds) * time.Second,
		handles:  handleMap,
		logger:   logger,
	}

	return p, recoveredCount
}

// MarkDirty flags the state as needing a checkpoint and, if no flush is
// already scheduled, arms a timer to write one after the configured
// checkpoint interval. A burst of MarkDirty calls within one interval
// coalesces into a single write, per Section 3.18's debounce description.
func (p *Persistence) MarkDirty() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.dirty = true
	if p.timer == nil {
		p.timer = time.AfterFunc(p.interval, p.flushFromTimer)
	}
}

// flushFromTimer is the debounce timer's callback. It runs on its own
// goroutine (per time.AfterFunc semantics), so it takes the lock itself
// rather than assuming the caller holds it.
func (p *Persistence) flushFromTimer() {
	p.mu.Lock()
	p.timer = nil
	shouldFlush := p.dirty
	p.mu.Unlock()

	if shouldFlush {
		if err := p.WriteCheckpoint(); err != nil {
			p.logger.Error("debounced state checkpoint failed", map[string]any{"error": err.Error()})
		}
	}
}

// WriteCheckpoint unconditionally serializes the current handle map to disk
// via the atomic temp-and-rename procedure, regardless of the dirty flag.
// Used by the debounce timer, by graceful shutdown (Section 3.24), and
// directly by tests that need a deterministic, synchronous flush.
func (p *Persistence) WriteCheckpoint() error {
	snapshot := p.handles.Snapshot()

	state := PersistedState{Handles: make(map[string]PersistedHandleState, len(snapshot))}
	for handle, handleState := range snapshot {
		baselines := make(map[string]PersistedSignature, len(handleState.Baselines))
		for target, sig := range handleState.Baselines {
			baselines[target] = PersistedSignature{ModTime: sig.ModTime, Size: sig.Size}
		}

		state.Handles[handle] = PersistedHandleState{
			Baselines:    baselines,
			LastActivity: handleState.LastActivity,
		}
	}

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.dirty = false
	p.mu.Unlock()

	if p.logger != nil {
		p.logger.Debug("state checkpoint written", map[string]any{
			"bytes":   len(encoded),
			"handles": len(state.Handles),
		})
	}

	return atomicWriteFile(p.path, encoded, 0o644)
}

// persistence_test.go covers design spec Section 8.1.8, dropping the
// branch-file and lazy-adoption rows per IMPLEMENTATION-PROMPT.md Section 2
// (branching omitted from this build, so there are no branch files to
// reconcile or adopt).
package main

import (
	"os"
	"testing"
	"time"
)

// TestPersistence_WriteCheckpointIncludesHandlesAndBaselines covers "State
// written at shutdown": the checkpoint contains live handles, their read
// baselines, and last-activity times. WriteCheckpoint is the exact function
// main.go calls on stdin EOF, so calling it directly here exercises the same
// code path shutdown does.
func TestPersistence_WriteCheckpointIncludesHandlesAndBaselines(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "project-foo", "content", "A project")

	if err := b.Persist.WriteCheckpoint(); err != nil {
		t.Fatalf("WriteCheckpoint failed: %v", err)
	}

	loaded := loadPersistedState(b.Config.StateFilePath(), b.Logger)
	state, ok := loaded.Handles[handle]
	if !ok {
		t.Fatalf("expected handle %q in persisted state", handle)
	}
	if state.LastActivity.IsZero() {
		t.Fatalf("expected a non-zero last-activity time")
	}
	if _, ok := state.Baselines["project-foo"]; !ok {
		t.Fatalf("expected a recorded baseline for project-foo, got %#v", state.Baselines)
	}
}

// TestPersistence_DebouncedCheckpoint covers "Debounced checkpoint": a burst
// of memory operations does not produce an immediate write; the state file
// appears only after the checkpoint interval elapses.
func TestPersistence_DebouncedCheckpoint(t *testing.T) {
	b := newTestBridge(t)
	b.Persist.interval = 200 * time.Millisecond

	// Minting a handle marks the state dirty and arms the debounce timer.
	startConversation(t, b)

	if fileExists(b.Config.StateFilePath()) {
		t.Fatalf("expected no checkpoint written yet (debounce window not elapsed)")
	}

	time.Sleep(400 * time.Millisecond)

	if !fileExists(b.Config.StateFilePath()) {
		t.Fatalf("expected a debounced checkpoint to have been written by now")
	}
}

// TestPersistence_LoadAndReconcileAtStartup covers "Load and reconcile at
// startup": handles and read baselines survive a simulated bridge restart —
// a fresh HandleMap loading the same state file recovers them, and the
// recovered handle works without re-initialization.
func TestPersistence_LoadAndReconcileAtStartup(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "project-foo", "content", "A project")

	if err := b.Persist.WriteCheckpoint(); err != nil {
		t.Fatalf("WriteCheckpoint failed: %v", err)
	}

	freshHandles := NewHandleMap(b.Config.Handle.IDLength)
	_, recovered := NewPersistence(b.Config, freshHandles, b.Logger)

	if recovered != 1 {
		t.Fatalf("expected 1 recovered handle, got %d", recovered)
	}

	if verr := freshHandles.Validate(handle); verr != nil {
		t.Fatalf("expected the restored handle to validate successfully: %v", verr)
	}

	if _, hadBaseline := freshHandles.Baseline(handle, "project-foo"); !hadBaseline {
		t.Fatalf("expected the restored handle to carry its project-foo baseline")
	}
}

// TestPersistence_CorruptStateFileStartsClean covers "Corrupt state file":
// a truncated/invalid state file causes a clean start (0 recovered handles)
// with a warning logged, rather than a startup failure.
func TestPersistence_CorruptStateFileStartsClean(t *testing.T) {
	b := newTestBridge(t)

	if err := os.WriteFile(b.Config.StateFilePath(), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	freshHandles := NewHandleMap(b.Config.Handle.IDLength)
	_, recovered := NewPersistence(b.Config, freshHandles, b.Logger)

	if recovered != 0 {
		t.Fatalf("expected 0 recovered handles from a corrupt state file, got %d", recovered)
	}
}

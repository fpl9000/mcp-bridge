// handles_test.go covers design spec Section 8.1.1: memory_start_conversation.
package main

import (
	"regexp"
	"testing"
)

// TestStartConversation_BasicResponse covers the "Basic conversation start"
// row: no params, returns a unique 8-char handle, core content, and a
// derived index — all three in one round trip per the implementation
// prompt's scope table.
func TestStartConversation_BasicResponse(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)

	handle, _ := response["handle"].(string)
	if len(handle) != 8 {
		t.Fatalf("expected an 8-character handle, got %q", handle)
	}

	if _, ok := response["core"]; !ok {
		t.Fatalf("response missing core field: %#v", response)
	}

	if _, ok := response["index"]; !ok {
		t.Fatalf("response missing index field: %#v", response)
	}
}

// TestStartConversation_HandleUniqueness covers "Handle uniqueness": 1,000
// minted handles are all distinct.
func TestStartConversation_HandleUniqueness(t *testing.T) {
	b := newTestBridge(t)

	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		handle := b.Handles.Mint()
		if seen[handle] {
			t.Fatalf("duplicate handle minted: %q", handle)
		}
		seen[handle] = true
	}
}

// TestStartConversation_HandleFormat covers "Handle format": the handle
// matches [0-9a-z]{8}.
func TestStartConversation_HandleFormat(t *testing.T) {
	b := newTestBridge(t)

	pattern := regexp.MustCompile(`^[0-9a-z]{8}$`)
	handle := startConversation(t, b)

	if !pattern.MatchString(handle) {
		t.Fatalf("handle %q does not match [0-9a-z]{8}", handle)
	}
}

// TestStartConversation_EmptyMemoryStore covers "Empty memory store": no
// core.md, empty blocks dir. Returns empty core and empty index array,
// without error (the implementation prompt's cold-start tolerance
// requirement).
func TestStartConversation_EmptyMemoryStore(t *testing.T) {
	b := newTestBridge(t)

	response := callToolJSON(t, b.HandleMemoryStartConversation, nil)

	if ok, present := response["ok"]; present && ok == false {
		t.Fatalf("expected success response on a cold-start store, got error: %#v", response)
	}

	core, _ := response["core"].(string)
	if core != "" {
		t.Fatalf("expected empty core string on cold start, got %q", core)
	}

	indexField, ok := response["index"].(map[string]any)
	if !ok {
		t.Fatalf("expected index to be an object, got %#v", response["index"])
	}

	blocks, ok := indexField["blocks"].([]any)
	if !ok {
		t.Fatalf("expected index.blocks to be an array, got %#v", indexField["blocks"])
	}

	if len(blocks) != 0 {
		t.Fatalf("expected an empty index array on cold start, got %d entries", len(blocks))
	}
}

// TestStartConversation_HandleRegisteredInState covers "Handle registered in
// state": after minting, the handle is present in the map with an empty
// baseline set (this build has no branch map to assert on — see
// IMPLEMENTATION-PROMPT.md Section 2).
func TestStartConversation_HandleRegisteredInState(t *testing.T) {
	b := newTestBridge(t)

	handle := startConversation(t, b)

	if verr := b.Handles.Validate(handle); verr != nil {
		t.Fatalf("newly minted handle failed validation: %v", verr)
	}
}

// TestStartConversation_SurvivesCompactionRoundTrip covers "Handle survives
// compaction round-trip": the same handle, presented again in a fresh tool
// call, is accepted by every memory tool without re-initialization.
func TestStartConversation_SurvivesCompactionRoundTrip(t *testing.T) {
	b := newTestBridge(t)

	handle := startConversation(t, b)

	// Simulate a later, unrelated tool call in a "fresh" context that still
	// carries the same handle string (as if compaction had wiped everything
	// except one earlier tool response containing it).
	getCoreResponse := callToolJSON(t, b.HandleMemoryGetCore, map[string]any{"handle": handle})
	if getCoreResponse["ok"] == false {
		t.Fatalf("handle rejected after simulated compaction: %#v", getCoreResponse)
	}
}

// TestStartConversation_StateCheckpointOnCreation covers "State checkpoint on
// creation": .bridge-state.json is updated (debounced) to include the new
// handle. The test forces a synchronous checkpoint rather than waiting on
// the debounce timer, since WriteCheckpoint is the same code path the timer
// invokes.
func TestStartConversation_StateCheckpointOnCreation(t *testing.T) {
	b := newTestBridge(t)

	handle := startConversation(t, b)

	if err := b.Persist.WriteCheckpoint(); err != nil {
		t.Fatalf("WriteCheckpoint failed: %v", err)
	}

	loaded := loadPersistedState(b.Config.StateFilePath(), b.Logger)
	if _, present := loaded.Handles[handle]; !present {
		t.Fatalf("checkpoint did not include newly minted handle %q", handle)
	}
}

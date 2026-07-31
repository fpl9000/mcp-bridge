// memmutex.go defines the single process-wide mutex that serializes all
// memory file I/O, per design spec Section 3.17 ("The memory mutex (ordinary
// operation)"). With branching omitted from this build (see Section 2 of
// IMPLEMENTATION-PROMPT-minimal.md), there is no merge mutex and no race-routing —
// this mutex's only job is to make sure concurrent tool calls never
// interleave reads and writes on the same files, so last-writer-wins is the
// entire concurrency story.
package main

import "sync"

// MemMutex serializes memory tool handlers. It is a thin named wrapper
// around sync.Mutex (rather than a bare field) so every call site reads as
// "the memory mutex" and so the type shows up distinctly in profiling and
// documentation.
type MemMutex struct {
	mu sync.Mutex
}

// Lock acquires the memory mutex. Handlers must always use the pointer
// receiver (never copy a MemMutex by value) — go vet's copylocks check
// catches accidental copies.
func (m *MemMutex) Lock() {
	m.mu.Lock()
}

// Unlock releases the memory mutex.
func (m *MemMutex) Unlock() {
	m.mu.Unlock()
}

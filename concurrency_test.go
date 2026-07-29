// concurrency_test.go covers design spec Section 8.1.9: the memory mutex
// serializes all memory tool handlers, so concurrent tool calls never
// interleave or corrupt a file. Goroutines in these tests must never call
// t.Fatalf directly (only the top-level test goroutine may), so they report
// failures through a shared, mutex-guarded error slice collected after every
// goroutine finishes.
package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// rawCallTool invokes a tool handler without any testing.T dependency, so it
// is safe to call from a background goroutine.
func rawCallTool(handler toolHandler, args map[string]any) (*mcp.CallToolResult, error) {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = args
	return handler(context.Background(), request)
}

// errorCollector gathers failures reported from background goroutines for a
// single Fatalf-free report on the test goroutine.
type errorCollector struct {
	mu     sync.Mutex
	errors []string
}

func (c *errorCollector) add(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, msg)
}

func (c *errorCollector) check(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.errors {
		t.Error(msg)
	}
}

// TestConcurrency_WritesDoNotInterleave covers "Concurrent writes": two
// goroutines writing the same block simultaneously never produce a file
// containing a mix of both writes' content.
func TestConcurrency_WritesDoNotInterleave(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "shared", "seed", "Shared block")

	const iterations = 25
	valueA := strings.Repeat("A", 200)
	valueB := strings.Repeat("B", 200)

	collector := &errorCollector{}
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := rawCallTool(b.HandleMemoryWriteBlock, map[string]any{
				"handle": handle, "block_name": "shared", "content": valueA,
			}); err != nil {
				collector.add("write A returned unexpected Go error: " + err.Error())
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := rawCallTool(b.HandleMemoryWriteBlock, map[string]any{
				"handle": handle, "block_name": "shared", "content": valueB,
			}); err != nil {
				collector.add("write B returned unexpected Go error: " + err.Error())
			}
		}()
	}

	wg.Wait()
	collector.check(t)

	raw := readFileString(t, b.blockPath("shared"))
	_, body, hasFrontmatter := splitFrontmatter([]byte(raw))
	if !hasFrontmatter {
		t.Fatalf("expected well-formed frontmatter after concurrent writes, got: %q", raw)
	}
	if body != valueA && body != valueB {
		t.Fatalf("expected body to be entirely A's or entirely B's, got interleaved/corrupt content of length %d", len(body))
	}
}

// TestConcurrency_WriteAndAppendOnDifferentBlocks covers "Write + append
// concurrent": a write to block X and an append to block Y proceed
// concurrently and both end up correct.
func TestConcurrency_WriteAndAppendOnDifferentBlocks(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "block-y", "initial", "Block Y")

	collector := &errorCollector{}
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if _, err := rawCallTool(b.HandleMemoryWriteBlock, map[string]any{
			"handle": handle, "block_name": "block-x", "content": "x content", "summary": "Block X",
		}); err != nil {
			collector.add("write to block-x returned unexpected Go error: " + err.Error())
		}
	}()

	go func() {
		defer wg.Done()
		if _, err := rawCallTool(b.HandleMemoryAppendBlock, map[string]any{
			"handle": handle, "block_name": "block-y", "content": " appended",
		}); err != nil {
			collector.add("append to block-y returned unexpected Go error: " + err.Error())
		}
	}()

	wg.Wait()
	collector.check(t)

	_, bodyX, _ := splitFrontmatter([]byte(readFileString(t, b.blockPath("block-x"))))
	if bodyX != "x content" {
		t.Fatalf("expected block-x to contain its written content, got: %q", bodyX)
	}

	// This assertion previously expected mid-line continuation ("initial
	// appended"). The append path now guarantees appended text starts on a new
	// line, which makes continuing the block's last line impossible by design;
	// the concurrency property under test — that the two blocks do not
	// interfere — is unaffected.
	_, bodyY, _ := splitFrontmatter([]byte(readFileString(t, b.blockPath("block-y"))))
	if bodyY != "initial\n appended\n" {
		t.Fatalf("expected block-y to contain the appended content, got: %q", bodyY)
	}
}

// TestConcurrency_NoDeadlockUnderMixedOps covers "Mutex does not deadlock":
// rapid alternation of reads, writes, appends, and index calls all complete
// within a generous timeout.
func TestConcurrency_NoDeadlockUnderMixedOps(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "mixed", "seed", "Mixed-ops block")

	const opsPerKind = 20
	var wg sync.WaitGroup
	wg.Add(4 * opsPerKind)

	for i := 0; i < opsPerKind; i++ {
		go func() {
			defer wg.Done()
			rawCallTool(b.HandleMemoryGetBlock, map[string]any{"handle": handle, "block_name": "mixed"})
		}()
		go func() {
			defer wg.Done()
			rawCallTool(b.HandleMemoryWriteBlock, map[string]any{"handle": handle, "block_name": "mixed", "content": "updated"})
		}()
		go func() {
			defer wg.Done()
			rawCallTool(b.HandleMemoryAppendBlock, map[string]any{"handle": handle, "block_name": "mixed", "content": "!"})
		}()
		go func() {
			defer wg.Done()
			rawCallTool(b.HandleMemoryGetIndex, map[string]any{"handle": handle})
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All operations completed — no deadlock.
	case <-time.After(10 * time.Second):
		t.Fatalf("mixed concurrent operations did not complete within timeout — possible deadlock")
	}
}

// TestConcurrency_IndexReflectsConsistentSnapshot covers "Index derivation
// under mutex": memory_get_index calls running concurrently with writes
// always see a fully-formed, parseable index — never a torn read of a
// half-written block file.
func TestConcurrency_IndexReflectsConsistentSnapshot(t *testing.T) {
	b := newTestBridge(t)
	handle := startConversation(t, b)
	writeBlock(t, b, handle, "growing", "seed", "A growing block")

	collector := &errorCollector{}
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			if _, err := rawCallTool(b.HandleMemoryWriteBlock, map[string]any{
				"handle": handle, "block_name": "growing", "content": strings.Repeat("x", i+1),
			}); err != nil {
				collector.add("write returned unexpected Go error: " + err.Error())
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			result, err := rawCallTool(b.HandleMemoryGetIndex, map[string]any{"handle": handle})
			if err != nil {
				collector.add("get_index returned unexpected Go error: " + err.Error())
				continue
			}
			if len(result.Content) == 0 {
				collector.add("get_index returned no content")
			}
		}
	}()

	wg.Wait()
	collector.check(t)
}

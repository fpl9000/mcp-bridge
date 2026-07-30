// tools.go registers the bridge's eight in-scope memory-aware tools with the
// mcp-go server, per design spec Section 3.3 and the pruned tool list in
// IMPLEMENTATION-PROMPT.md Section 3. spawn_agent, check_agent, run_command,
// and memory_run_maintenance are out of scope for this build and are not
// registered.
package main

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterTools registers all eight memory-aware tools and their handlers.
// Every tool other than memory_start_conversation declares handle as a
// required first parameter (Section 3.7).
func RegisterTools(s *server.MCPServer, b *Bridge) {
	s.AddTool(
		mcp.NewTool("memory_start_conversation",
			mcp.WithDescription("Start a memory conversation and receive a handle, the current core "+
				"content, and the derived block index in one round trip. Call this once at the start "+
				"of any conversation that will use memory, before any other memory tool. Pass the "+
				"returned handle to every subsequent memory tool call. If any memory tool returns a "+
				"handle error, call this again to obtain a fresh handle and retry the operation."),
		),
		b.HandleMemoryStartConversation,
	)

	s.AddTool(
		mcp.NewTool("memory_get_core",
			mcp.WithDescription("Return the core memory content. Call at the start of every conversation, "+
				"after memory_start_conversation. If changed_since_last_read is true, the content has "+
				"changed since you last read it — treat earlier conclusions drawn from the previous "+
				"version as potentially stale."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
		),
		b.HandleMemoryGetCore,
	)

	s.AddTool(
		mcp.NewTool("memory_write_core",
			mcp.WithDescription("Replace the core memory content. Provide the COMPLETE document — this "+
				"is a full replacement, not an edit. Keep core under ~1,000 tokens; move detailed "+
				"content into blocks."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Complete replacement content")),
		),
		b.HandleMemoryWriteCore,
	)

	s.AddTool(
		mcp.NewTool("memory_get_index",
			mcp.WithDescription("Return the index of memory blocks: each block's name, one-line summary, "+
				"and last-updated time. Use it to decide which blocks are relevant to the current "+
				"conversation, then load them with memory_get_block."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
		),
		b.HandleMemoryGetIndex,
	)

	s.AddTool(
		mcp.NewTool("memory_get_block",
			mcp.WithDescription("Return a memory block's content by name. Block names come from "+
				"memory_get_index. If changed_since_last_read is true, the content has changed since "+
				"you last read it — treat earlier conclusions drawn from the previous version as "+
				"potentially stale."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
			mcp.WithString("block_name", mcp.Required(), mcp.Description(`Block name (e.g., "project-foo")`)),
		),
		b.HandleMemoryGetBlock,
	)

	s.AddTool(
		mcp.NewTool("memory_write_block",
			mcp.WithDescription("Replace a memory block's content, or create a new block. Provide the "+
				"COMPLETE content — this is a full replacement, not an edit. The optional summary is a "+
				"one-line description shown in the index; it is REQUIRED when creating a new block, "+
				"and preserved unchanged if omitted when updating an existing one."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
			mcp.WithString("block_name", mcp.Required(), mcp.Description("Block name")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Complete replacement body")),
			mcp.WithString("summary", mcp.Description("One-line description for the index")),
		),
		b.HandleMemoryWriteBlock,
	)

	s.AddTool(
		mcp.NewTool("memory_append_block",
			mcp.WithDescription("Append text to a memory block. The bridge guarantees the "+
				"appended text starts on a new line, so no leading newline is needed. Never "+
				"creates a block: use memory_write_block for first creation."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
			mcp.WithString("block_name", mcp.Required(), mcp.Description("Block name")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Text to append")),
		),
		b.HandleMemoryAppendBlock,
	)

	s.AddTool(
		mcp.NewTool("memory_append_episodic",
			mcp.WithDescription("Append an entry to the episodic log (the chronological record of "+
				"significant conversations). The bridge files the entry under the current month "+
				"and writes the dated heading itself. Pass a short title and a 2-5 sentence "+
				"summary as the body; do not include a markdown heading in content."),
			mcp.WithString("handle", mcp.Required(), mcp.Description("Handle from memory_start_conversation")),
			mcp.WithString("title", mcp.Required(), mcp.Description("Short entry title; the bridge "+
				"renders it as \"## YYYY-MM-DD — Title\" using its own clock")),
			mcp.WithString("content", mcp.Required(), mcp.Description("The entry body, without a "+
				"heading line")),
		),
		b.HandleMemoryAppendEpisodic,
	)
}

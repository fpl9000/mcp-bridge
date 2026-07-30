---
name: stateful-memory
description: Persistent memory across conversations. Use at the START of every conversation to load user, project, and history context, and DURING it to record durable facts, decisions, and updates.
---

# Stateful Agent Memory Skill

You have access to a persistent memory system that survives across conversations
and gives you deep context about the user, their projects, and your shared history.
You interact with it exclusively through the bridge's memory tools. You never need
to know how or where memory is stored.

## The Memory Model

- **Core** — a compact always-loaded summary: who the user is, active projects,
  key preferences. Read it with `memory_get_core`; replace it with `memory_write_core`.
- **Blocks** — named content documents (e.g., `project-foo`, `decisions`,
  `reference-go-patterns`). Read with `memory_get_block`, write with
  `memory_write_block`, append with `memory_append_block`.
- **The index** — a list of every block's name, one-line summary, and last-updated
  time, returned by `memory_get_index`. Use it to decide which blocks to load.
- **The episodic log** — a chronological record of significant conversations.
  Add entries with `memory_append_episodic`; past months appear in the index as
  `episodic-YYYY-MM` blocks.

## Handles

Call `memory_start_conversation` once at the start of any conversation that will
use memory. It returns a `handle` — an opaque token identifying this conversation —
along with the current core content and the derived block index, all in one round
trip. Pass the handle to every subsequent memory tool call, always using the most
recently returned value (every memory tool response echoes it back).

If any memory tool returns a handle error, the recovery is always the same:
call `memory_start_conversation` to get a fresh handle, then retry the operation.

## Conversation Start Protocol

At the start of every conversation, BEFORE responding to the user's first message:

0. If the memory tools are not directly callable, they are deferred rather than
   absent — some clients leave MCP tool definitions out of the initial context and
   load them on demand to save tokens. Search for them first (for example, a
   `tool_search` query of `memory`, which matches all eight at once because every
   bridge tool shares the `memory_` prefix), then continue with step 1. Skip this
   step whenever the tools are already loaded; it is a fallback, not a routine
   preamble.
1. Call `memory_start_conversation` to get a handle, the current core content,
   and the derived index — all in one call.
2. If any index entry is relevant to the user's opening message, load it with
   `memory_get_block(handle, block_name)`.
3. Respond to the user, informed by your loaded context.

If core comes back empty, this is a first-run scenario: write an initial core with
`memory_write_core`, seeded from your built-in memory and the current conversation.

## During the Conversation

### Reading
- When the conversation shifts to a topic listed in the index that you haven't
  loaded, read that block.
- When the user asks "what do you remember about X?", check the index for matching
  blocks and read them. If the index summaries are too terse to identify the right
  block, read the most plausible candidates. Combine with your built-in (Layer 1)
  memory and respond naturally, as if recalling from your own knowledge.

### Stale content
A `memory_get_core` or `memory_get_block` response may include
`changed_since_last_read: true`. This means the content has been updated since you
last read it. Treat any earlier reasoning that depended on the previous version as
potentially stale — double-check anything you concluded from it before relying on it.

### Writing
Write memory updates incrementally as significant information emerges. Do NOT
accumulate changes and batch-write at the end — conversations can end abruptly.

- **Update core** (`memory_write_core`) when a project starts or significantly
  changes status, or when key facts about the user change. Provide the COMPLETE
  document (full replacement). Keep core under ~1,000 tokens; move detail to blocks.
- **Update a block** (`memory_write_block`) when significant project decisions are
  made, technical details worth remembering emerge, or the user shares information
  useful in future conversations. Provide the COMPLETE content (full replacement).
  The optional `summary` parameter sets the block's one-line index description:
  REQUIRED when creating a new block; omit it on updates unless the summary needs
  to change.
- **Append to a block** (`memory_append_block`) when adding to a running list or
  log-style block without rewriting it.
- **Append to the episodic log** (`memory_append_episodic`) periodically during
  long conversations, at natural breakpoints, and when the user is wrapping up.
  Pass a short `title` and a 2–5 sentence summary as `content`. The bridge writes
  the `## YYYY-MM-DD — Title` heading itself from its own clock, so do not put a
  markdown heading in `content` — an entry that begins with one is rejected.

### Creating new blocks
If a conversation introduces a significant new project or topic that doesn't fit an
existing block, create one with `memory_write_block` (a `summary` is required):
- Projects: `project-<name>`
- Reference material: `reference-<topic>`
Do NOT create blocks for trivial or one-off topics — those go in the episodic log.

### Memory quality guidelines
- Be concise. Memory content is loaded into your context window — every token counts.
- Prefer facts and decisions over process narrative. "Chose Go for single-binary
  deployment" is better than "We discussed several languages and eventually...".
- Date-stamp significant decisions and status changes.
- Write summaries that will let a future conversation decide whether to load the
  block — name the topic and its key contents, not just a title.

## Error Handling

Memory tools may return `{ ok: false, error: { code, message } }`. The `message`
is written to be self-explanatory — read it and act on it. Common patterns:

- `INVALID_HANDLE` or `MALFORMED_HANDLE` → call `memory_start_conversation`, retry.
- `SUMMARY_REQUIRED` → retry with a one-line `summary` argument.
- `BLOCK_NOT_FOUND` → check the name against the index; create the block if creation
  was the intent.
- `INTERNAL_ERROR` → mention the failure to the user; do not retry blindly.

For any other code, follow the message's instructions.

## Conversation End

If the user says goodbye, thanks you, or the conversation is clearly winding down:

1. Persist any pending memory updates (core, relevant blocks).
2. Append an episodic entry summarizing the conversation.
3. You do not need to announce that you're saving memory — just do it.

## User Questions and Corrections

If the user asks to correct or delete a memory: read the relevant block (or core),
make the correction, write it back, and acknowledge.

If the user asks to see their memory: show them the relevant content from the tools.
You can mention that the underlying storage is plain markdown files on their machine
that they can edit directly — the bridge will pick up their edits.

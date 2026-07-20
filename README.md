# mcp-bridge

A single-binary MCP (Model Context Protocol) server that gives Claude Desktop
persistent, local, Layer 2 memory: a compact always-loaded "core" document plus
named content blocks and a chronological episodic log, all stored as plain
markdown files on disk that a human can read and edit directly.

This is the **minimal first cut** of the bridge described in the Stateful Agent
System design (see `docs/design-spec/`): the memory subsystem only. Sub-agent
spawning (`spawn_agent`, `check_agent`), local command execution
(`run_command`), and memory maintenance/merging/branching are all out of scope
for this build. Concurrent writes to the same memory target use simple
last-writer-wins semantics; there is no branch-and-merge race resolution yet.

## Tools

The bridge registers eight MCP tools, all operating on memory *concepts*
(handles, core, blocks, the index) rather than file paths — the bridge owns the
on-disk layout entirely, and the LLM never sees it.

| Tool | Purpose |
| --- | --- |
| `memory_start_conversation` | Mint a fresh handle; return it along with core content and the derived block index in one round trip. Call once per conversation, before any other memory tool. |
| `memory_get_core` | Return the core memory document. |
| `memory_write_core` | Replace the core memory document (full replacement). |
| `memory_get_index` | Return the derived index: every block's name, one-line summary, and last-updated time. |
| `memory_get_block` | Return a block's body content by name. |
| `memory_write_block` | Replace a block's content, or create a new block (a summary is required on creation). |
| `memory_append_block` | Append text to an existing block (never creates one). |
| `memory_append_episodic` | Append a dated entry to the current month's episodic log; the bridge handles file creation and month rotation. |

Every memory tool takes `handle` as a required first parameter and echoes it
back in every response, success or failure.

## Building

The bridge is pure Go with no CGO and no external C libraries, so it
cross-compiles cleanly to the shippable target, a Windows binary:

```sh
# Native build, for local testing on this machine:
go build -o mcp-bridge .

# The shippable artifact — a single static Windows executable:
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mcp-bridge.exe .
```

Run `./mcp-bridge --version` to confirm the binary works and print its version.

Run the test suite with:

```sh
go test ./...
```

## Configuration

The bridge reads a YAML configuration file. The file location is resolved in
priority order:

1. The `--config` command-line flag.
2. The `MCP_BRIDGE_CONFIG` environment variable.
3. A built-in default path (`C:\franl\.claude-agent-memory\bridge-config.yaml`
   on the shippable Windows target).

If no file exists yet at the resolved location, the bridge seeds one from the
embedded default (`bridge-config.yaml` at the repo root) and continues with
those default values. The default config also documents, section by section,
which parts of the schema this build actually acts on — the schema includes
every section from the full twelve-tool design (`async`, `sub_agent`,
`run_command`, `branching`, `maintenance`, `claude_cli`, ...) so the config
file's shape doesn't need to change when those features land in a later
build, but only `memory`, `handle`, `persistence`, and `logging` are honored
and validated today.

Key settings for this build:

```yaml
memory:
  directory: "C:\\franl\\.claude-agent-memory"   # The memory root; created if absent
  summary_max_length: 200                        # Cap on block summary length

handle:
  id_length: 8            # Length of minted handles
  retention_days: 60       # Unused until the deferred maintenance sweep exists

persistence:
  state_file: ".bridge-state.json"        # Relative to memory.directory
  checkpoint_interval_seconds: 5          # Debounce window for state checkpoints

logging:
  file: "C:\\franl\\.claude-agent-memory\\bridge.log"
  level: "info"            # debug, info, warn, error
```

If `branching.enabled: true` is set in the config, the bridge logs a startup
warning rather than silently pretending to branch — this build always uses
last-writer-wins semantics regardless of that setting.

## Memory store layout

Under `memory.directory`, the bridge maintains:

```
<memory.directory>/
├── bridge-config.yaml       # Your copy of the configuration (not memory content)
├── .bridge-state.json       # Bridge-private persisted state (not memory content)
├── bridge.log               # Bridge server log (not memory content)
├── core.md                  # Always-loaded identity/context document (no frontmatter)
└── blocks/
    ├── project-foo.md       # A named content block (YAML frontmatter + markdown body)
    ├── decisions.md         # Cross-project architectural decisions
    └── episodic-2026-06.md  # A monthly episodic log, auto-created and rotated
```

Block files carry a small YAML frontmatter header (`summary`, `updated_at`)
that the bridge manages transparently — the LLM never sees it, but a human can
open any block file in a text editor and read it directly. `core.md` has no
frontmatter.

## How Claude Desktop launches the bridge

Claude Desktop spawns `mcp-bridge.exe` as a subprocess and communicates with it
over stdio using the MCP protocol (JSON-RPC over stdin/stdout). Configure it in
Claude Desktop's MCP server settings, e.g.:

```json
{
  "mcpServers": {
    "mcp-bridge": {
      "command": "C:\\path\\to\\mcp-bridge.exe",
      "args": ["--config", "C:\\franl\\.claude-agent-memory\\bridge-config.yaml"]
    }
  }
}
```

The bridge logs to the configured log file, never to stdout — stdout is
reserved exclusively for the MCP protocol stream. When Claude Desktop closes,
it closes the bridge's stdin, which the bridge treats as its shutdown signal:
it writes a final state checkpoint and exits.

## Memory skill

`skill/SKILL.md` is the companion Claude Desktop skill that teaches Claude
*when* to read and write memory using these tools — conversation-start
protocol, write triggers, stale-content handling, and error recovery. Package
it as a `.zip` containing just that file and upload it via Claude Desktop >
Settings > Capabilities.

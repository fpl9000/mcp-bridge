# Running the minimal `mcp-bridge` build on Claude Code on the web

This repo has been **pre-staged** so the initial implementation can be handed to
Claude Code on the web (CC-web), which runs in an isolated cloud VM and executes
asynchronously — no need to keep a local machine powered on.

## What is already here (pre-staged, not deliverables)

- `IMPLEMENTATION-PROMPT-minimal.md` — the CC-web-adapted implementation prompt. Give
  this to Claude Code as the task. (The original, local-Windows version lives in
  `fpl9000/ai-skills` under `docs/stateful-agent-design/`.)
- `docs/design-spec/` — a read-only working copy of the authoritative design
  docs from `fpl9000/ai-skills`. Claude Code reads these in place. They are
  **reference only** and should not be modified or shipped.
- `CLAUDE.md` — the coding conventions for this build: a faithful extract of the
  authoritative global `~/.claude/CLAUDE.md`, limited to what applies in the
  Linux VM (Windows/Cygwin-local rules omitted).
- `docs/CC-WEB-SETUP.md` — this file.

## What Claude Code will add (the deliverable)

The Go sources at the repo root (`main.go`, `config.go`, `tools.go`, and the
rest listed in the prompt), `go.mod`, `go.sum`, `bridge-config.yaml`, the
project `README.md`, and `skill/SKILL.md`. Work happens on a branch and lands as
a pull request against `main`.

## Two setup steps you must do in the CC-web UI

1. **Connect this repo** (`fpl9000/mcp-bridge`) as the session's repository.
2. **Widen the network allowlist** so the Go toolchain can fetch modules. The
   two dependencies (`github.com/mark3labs/mcp-go` and `gopkg.in/yaml.v3`) are
   resolved through Go's module system, so allow at least:
   - `proxy.golang.org`
   - `sum.golang.org`
   - `github.com`
   Without these, `go mod download` / `go build` / `go test` fail at dependency
   resolution — the single most common cause of a failed run.

## After the run

Once the PR is merged, the pre-staged `docs/design-spec/` copy and this file may
be removed, since the canonical design lives in `fpl9000/ai-skills`.

**`IMPLEMENTATION-PROMPT-minimal.md` is not disposable and must be kept.** It is
the authoritative record of what the minimal build was asked to be, and Chapter
13 of the design cites it as such. Implementation prompts live in this
repository, one file per round of work, named `IMPLEMENTATION-PROMPT-<round>.md`;
a new round gets a new file, and an existing prompt is never rewritten to
describe different work.

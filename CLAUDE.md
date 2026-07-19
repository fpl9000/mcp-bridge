# Conventions for the `mcp-bridge` repo

> **PARTIAL STAND-IN — please replace.** This file is a best-effort
> reconstruction of Fran's coding conventions, assembled from stated
> preferences. It is **not** the authoritative `~/.claude/CLAUDE.md` that lives
> on Fran's local machine (that file is not present in this cloud VM). Where this
> file and the real global conventions differ, the real ones win. Fran intends to
> paste the authoritative content over this file.

## Code commenting

- Comment **densely**: aim for nearly as many lines of comment as lines of code.
- Comments should explain the *why* — the intent, the constraint, the non-obvious
  reason — not merely restate what the code literally does.
- A short explanatory remark is preferred over assuming the reader already
  understands. Err toward over-explaining rather than terseness.

## Commits

- Group work into a handful of **logically coherent commits** along architectural
  seams, so the history can be reviewed chunk by chunk.
- Do **not** produce one giant catch-all commit, and do **not** leave noisy
  "wip" history.
- Commit messages are descriptive and explain the *why* of the change, not just
  the *what*.
- Prefer pull requests as durable review checkpoints.

## Go specifics for this project

- Pure Go, no CGO, no external C libraries. The bridge must produce a single
  static binary.
- `gofmt` must report no files; `go vet ./...` must be clean. Watch `copylocks`
  in particular — the memory mutex is passed by pointer, never copied by value.
- The shippable artifact targets Windows (`GOOS=windows GOARCH=amd64`), since
  Claude Desktop runs on Windows 11.

## Prose (README, docs, comments)

- Precise prose that is not too terse. It is better to include an explanatory
  remark than to assume the reader understands everything.

# Coding conventions for the `mcp-bridge` repo

> Derived from Fran Litterio's authoritative `~/.claude/CLAUDE.md`. This copy
> keeps only the **environment-independent** conventions that apply when building
> this repo in the Claude Code on the web Linux VM. Sections of the original that
> are specific to Fran's local Windows + Cygwin machine are **omitted here because
> they do not apply in this cloud VM**: the OS environment, Cygwin pathname
> handling, the `es`/Everything search tool, the two home directories,
> Python-interpreter selection, the Bash and AutoHotkey scripting rules, and the
> local "git needs no credentials / SSH is configured" note (in this VM the shell
> is Linux and git authentication is handled by the environment's proxy). For
> anything environment-specific, follow `IMPLEMENTATION-PROMPT.md`.

## File encoding and newlines

- New files use UTF-8 encoding (no BOM).
- New files use UNIX-style newlines (a single line-feed).
- When modifying an existing file, match its existing encoding and newline
  convention; never convert one to the other.

## Indentation

- Use spaces for indentation, never tabs — in source code, markdown, and text.
- In a file that already mixes tabs and spaces, use spaces for any new
  indentation but do not convert the existing tabs to spaces.

## Source code style

- Keep source lines under 100 columns wide.
- Avoid single-character identifiers. In loops, use meaningful names such as
  `index`, `counter`, or `loopCount`.

## Comments (Fran wants thoroughly commented code)

- Always write well-commented source code.
- Comments are complete sentences.
- Put a comment on the line *above* the code it refers to, not on the same line.
  A same-line comment is acceptable only when it is very short, and such a short
  comment is exempt from the complete-sentence rule.
- Comments explain the *purpose and rationale* of the code — the "why" — rather
  than restating what the code plainly does.
- Do **not** comment trivial code, such as `import` statements or trivial local
  variable initialization, unless the comment explains something a developer
  genuinely needs to know.
- Do not address the user through code comments.

## Markdown (e.g. `README.md`, `skill/SKILL.md`)

- Long lines are fine; markdown renderers wrap them.
- Do not use `<br/>` for line breaks; use two trailing spaces at end of line.
- When creating a new markdown document, list Fran Litterio as the author.

## Building

- The shippable executable is a Windows binary, so its name ends in `.exe`
  (`mcp-bridge.exe`).
- Note this is a **console / stdio** MCP server, not a graphical app: do **not**
  pass `-ldflags "-H windowsgui"`. That flag suppresses the console window, which
  would break the stdio transport the bridge speaks over.

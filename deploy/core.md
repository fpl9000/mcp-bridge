# Core Memory — PLACEHOLDER (replace before relying on it)

This file is a placeholder installed by `deploy/install-bridge.sh`. It exists so
the deployment has a `core.md` to lay down, but its contents are not real memory.
Replace everything here with the actual initial core memory — a compact,
always-loaded summary of who the user is, their active projects, and key
preferences — then redeploy with `--force` (or edit the installed copy in place).

Note: `core.md` deliberately has **no** YAML frontmatter — it is the always-loaded
core document, not an indexed block. Leave it that way when you write the real
content.

Until this is replaced, the bridge treats it as an effectively empty core: a
valid cold start that harms nothing, but one that gives Claude no Layer 2 context
on the first conversation.

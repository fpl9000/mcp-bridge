#!/usr/bin/env bash
#
# install-bridge.sh — Deploy the mcp-bridge MCP server and its memory store on a
# Windows machine running Cygwin bash.
#
# This script performs the local, scriptable half of the deployment described in
# the Stateful Agent System design (Chapter 7). It lays down the bridge binary,
# creates the memory directory tree, installs the bridge configuration and an
# initial core.md, stages the memory skill archive, and registers the bridge as
# an MCP server in Claude Desktop.
#
# Installing the memory *skill* is deliberately NOT automated: Claude Desktop
# only accepts skills through its UI (Customize > Skills), so no script can drop
# a skill into place. The script therefore stages the skill archive next to the
# memory store and, as its final action, prints the exact manual upload steps.
#
# Invocation: run the script from the folder that also contains the four build
# artifacts it installs:
#
#     mcp-bridge.exe   bridge-config.yaml   core.md   stateful-memory.zip
#
# The script resolves those artifacts relative to its own location (not the
# caller's working directory), so it can be launched by absolute path or from
# anywhere; what matters is that the four files sit beside this script. If any
# one of them is absent, the script aborts before making any changes.
#
# Idempotency and safety: the script is safe to re-run. It never destroys data
# it cannot recover — it backs up an existing Claude Desktop config and an
# existing bridge-config.yaml before overwriting them, it merges (rather than
# replaces) the Claude Desktop MCP server map so other servers are preserved,
# and it refuses to clobber an already-seeded core.md unless invoked with
# --force. Directory creation and the exe copy are naturally idempotent.

# Abort on the first command failure, on any reference to an unset variable, and
# on a failure anywhere in a pipeline, so that a half-finished deployment can
# never be mistaken for a successful one.
set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# The memory root is fixed by the design spec (Chapter 7) and by the paths baked
# into bridge-config.yaml. It is written here in Windows form because that is the
# form Claude Desktop's configuration JSON must contain; every POSIX path the
# script needs for local file operations is derived from it with cygpath below.
readonly MEM_WIN='C:\franl\.claude-agent-memory'

# The bridge binary lives in a bin\mcp-bridge\ subfolder of the memory root, to
# match the "command" path in the design spec's §7.2 Claude Desktop example.
readonly BIN_SUBDIR='bin\mcp-bridge'

# The exact filenames of the four artifacts this script expects to find beside
# itself. These names are also what Claude Desktop and the bridge expect, so
# they are treated as fixed rather than configurable.
readonly ARTIFACT_EXE='mcp-bridge.exe'
readonly ARTIFACT_CONFIG='bridge-config.yaml'
readonly ARTIFACT_CORE='core.md'
readonly ARTIFACT_SKILL='stateful-memory.zip'

# The key under which the bridge is registered in claude_desktop_config.json.
# Re-using this exact key on a re-run updates the existing entry in place instead
# of creating a duplicate server.
readonly MCP_SERVER_KEY='mcp-bridge'

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

# log prints a progress line to stderr. Progress and errors go to stderr so that
# the script's normal chatter never gets confused with any value a caller might
# try to capture from stdout.
log() { printf '  %s\n' "$*" >&2; }

# die prints an error and exits non-zero. Because "set -e" is in effect, most
# failures abort on their own; die is for the conditions the script checks for
# explicitly (a missing artifact, a missing tool) and wants to explain clearly.
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

# --force permits overwriting an existing core.md. It exists so that a
# deliberate re-seed is possible, while the default run protects real memory
# content that may already be in place from an earlier deployment.
FORCE=0
for arg in "$@"; do
	case "$arg" in
		--force) FORCE=1 ;;
		-h|--help)
			# Print usage to stdout (this is the one case where stdout is the
			# right destination, because the user asked for help explicitly).
			sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
			exit 0
			;;
		*) die "unknown argument: $arg (use --help)" ;;
	esac
done

# ---------------------------------------------------------------------------
# Preconditions: environment and toolchain
# ---------------------------------------------------------------------------

# cygpath is the Cygwin utility that translates between Windows and POSIX path
# forms. Every path conversion below depends on it, so its absence means the
# script is not running under Cygwin and cannot proceed.
command -v cygpath >/dev/null 2>&1 || die "cygpath not found; this script must be run under Cygwin bash on Windows."

# APPDATA is inherited from the Windows environment and points at the per-user
# Roaming folder that holds Claude Desktop's configuration. Fall back to
# USERPROFILE\AppData\Roaming if APPDATA is somehow unset.
if [ -n "${APPDATA:-}" ]; then
	appdata_win="$APPDATA"
elif [ -n "${USERPROFILE:-}" ]; then
	appdata_win="$USERPROFILE\\AppData\\Roaming"
else
	die "Neither APPDATA nor USERPROFILE is set; cannot locate the Claude Desktop config directory."
fi

# ---------------------------------------------------------------------------
# Resolve all paths up front
# ---------------------------------------------------------------------------

# The artifact source directory is the directory containing this script, resolved
# to an absolute POSIX path so that copies work regardless of the caller's
# current working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Derive the POSIX forms of the memory directories from the Windows root. These
# are what mkdir and cp operate on locally.
MEM_POSIX="$(cygpath -u "$MEM_WIN")"
BLOCKS_POSIX="$MEM_POSIX/blocks"
BIN_POSIX="$MEM_POSIX/${BIN_SUBDIR//\\//}"

# The Claude Desktop configuration file lives under %APPDATA%\Claude. Convert the
# Roaming path to POSIX form and append the fixed subpath.
CLAUDE_DIR_POSIX="$(cygpath -u "$appdata_win")/Claude"
CLAUDE_CFG_POSIX="$CLAUDE_DIR_POSIX/claude_desktop_config.json"

# Pre-compute the Windows-form paths that must appear inside the Claude Desktop
# config JSON: the fully-qualified exe to launch and the --config file to pass.
EXE_WIN="$MEM_WIN\\$BIN_SUBDIR\\$ARTIFACT_EXE"
CONFIG_WIN="$MEM_WIN\\$ARTIFACT_CONFIG"

# A single timestamp reused for every backup this run makes, so that all backups
# from one invocation share a suffix and are easy to correlate.
STAMP="$(date +%Y%m%d-%H%M%S)"

# ---------------------------------------------------------------------------
# Preflight: every artifact must exist before we change anything
# ---------------------------------------------------------------------------

# Collect all missing artifacts first and report them together, so the user sees
# the complete list in one run rather than fixing them one at a time. No
# filesystem changes have been made at this point, so aborting here is clean.
log "Checking for the four required artifacts in: $SCRIPT_DIR"
missing=()
for artifact in "$ARTIFACT_EXE" "$ARTIFACT_CONFIG" "$ARTIFACT_CORE" "$ARTIFACT_SKILL"; do
	if [ ! -f "$SCRIPT_DIR/$artifact" ]; then
		missing+=("$artifact")
	fi
done
if [ "${#missing[@]}" -gt 0 ]; then
	die "Missing required artifact(s) beside the script: ${missing[*]}"
fi
log "All four artifacts present."

# ---------------------------------------------------------------------------
# Step 1: create the memory directory tree
# ---------------------------------------------------------------------------

# Creating blocks/ implicitly creates the memory root, and creating the bin
# subfolder gives the exe its home. mkdir -p is idempotent, so re-runs are fine.
# (The bridge would create the memory root and blocks/ itself on first launch,
# but doing it here means the tree exists before Claude Desktop ever starts.)
log "Creating memory directory tree under $MEM_WIN"
mkdir -p "$BLOCKS_POSIX"
mkdir -p "$BIN_POSIX"

# ---------------------------------------------------------------------------
# Step 2: install the bridge executable
# ---------------------------------------------------------------------------

# The exe is a build artifact, not user data, so overwriting an older copy is the
# intended behavior — a deployment should always ship the binary that came with
# it. cp -p preserves timestamps, which makes it obvious which build is deployed.
log "Installing $ARTIFACT_EXE -> $EXE_WIN"
cp -p "$SCRIPT_DIR/$ARTIFACT_EXE" "$BIN_POSIX/$ARTIFACT_EXE"

# ---------------------------------------------------------------------------
# Step 3: install bridge-config.yaml (back up any existing copy first)
# ---------------------------------------------------------------------------

# The config may have been hand-edited on a previous deployment, so before
# overwriting it the script preserves the current copy under a timestamped name.
# The backup makes the overwrite non-destructive: nothing is lost, and the
# freshly shipped config still lands.
config_target="$MEM_POSIX/$ARTIFACT_CONFIG"
if [ -f "$config_target" ]; then
	cp -p "$config_target" "$config_target.bak-$STAMP"
	log "Backed up existing config -> ${ARTIFACT_CONFIG}.bak-$STAMP"
fi
log "Installing $ARTIFACT_CONFIG -> $MEM_WIN\\$ARTIFACT_CONFIG"
cp -p "$SCRIPT_DIR/$ARTIFACT_CONFIG" "$config_target"

# ---------------------------------------------------------------------------
# Step 4: install core.md (never clobber real memory without --force)
# ---------------------------------------------------------------------------

# core.md is memory content, not a build artifact: once it holds a real seed it
# must not be silently overwritten by a redeploy. The default run therefore skips
# installation when a core.md already exists, and only --force (which still takes
# a backup) will replace it.
core_target="$MEM_POSIX/$ARTIFACT_CORE"
if [ -f "$core_target" ] && [ "$FORCE" -ne 1 ]; then
	log "Skipping $ARTIFACT_CORE: one already exists (use --force to overwrite; a backup would be made)."
else
	if [ -f "$core_target" ]; then
		cp -p "$core_target" "$core_target.bak-$STAMP"
		log "Backed up existing core.md -> ${ARTIFACT_CORE}.bak-$STAMP"
	fi
	log "Installing $ARTIFACT_CORE -> $MEM_WIN\\$ARTIFACT_CORE"
	cp -p "$SCRIPT_DIR/$ARTIFACT_CORE" "$core_target"
fi

# ---------------------------------------------------------------------------
# Step 5: stage the skill archive for manual upload
# ---------------------------------------------------------------------------

# The skill cannot be installed programmatically, so the script simply copies its
# archive next to the memory store where the user can find it, and remembers the
# staged Windows path to print in the closing instructions.
log "Staging $ARTIFACT_SKILL -> $MEM_WIN\\$ARTIFACT_SKILL"
cp -p "$SCRIPT_DIR/$ARTIFACT_SKILL" "$MEM_POSIX/$ARTIFACT_SKILL"
SKILL_STAGED_WIN="$MEM_WIN\\$ARTIFACT_SKILL"

# ---------------------------------------------------------------------------
# Step 6: register the bridge with Claude Desktop
# ---------------------------------------------------------------------------

# Ensure the Claude directory exists (a fresh Claude Desktop install creates it,
# but the script should not assume Claude Desktop has been launched yet).
mkdir -p "$CLAUDE_DIR_POSIX"

# The desired server entry is the same regardless of how it gets written: launch
# the bridge exe, passing the config file via --config.
log "Registering MCP server '$MCP_SERVER_KEY' in $CLAUDE_CFG_POSIX"

if command -v jq >/dev/null 2>&1; then
	# Preferred path: jq lets us MERGE the entry into any existing configuration,
	# preserving every other MCP server the user already has. jq's --arg emits
	# correctly escaped JSON strings, so the Windows backslashes are handled for
	# us and we never build JSON by hand.
	if [ -f "$CLAUDE_CFG_POSIX" ]; then
		cp -p "$CLAUDE_CFG_POSIX" "$CLAUDE_CFG_POSIX.bak-$STAMP"
		log "Backed up existing Claude Desktop config -> claude_desktop_config.json.bak-$STAMP"
		base_json="$(cat "$CLAUDE_CFG_POSIX")"
		# A zero-byte or whitespace-only file (which Claude Desktop can leave as a
		# stub) is not valid jq input, so treat that degenerate case as an empty
		# object exactly as if no config existed.
		if [ -z "${base_json//[[:space:]]/}" ]; then
			base_json='{}'
		fi
	else
		# No config yet: start from an empty object so the same jq program works.
		base_json='{}'
	fi

	# Set (create or replace) just our server key, leaving the rest of the
	# document untouched. The result is written to a temp file and moved into
	# place so a reader never sees a half-written config.
	printf '%s' "$base_json" | jq \
		--arg key "$MCP_SERVER_KEY" \
		--arg cmd "$EXE_WIN" \
		--arg cfg "$CONFIG_WIN" \
		'.mcpServers[$key] = { "command": $cmd, "args": ["--config", $cfg] }' \
		> "$CLAUDE_CFG_POSIX.tmp"
	mv "$CLAUDE_CFG_POSIX.tmp" "$CLAUDE_CFG_POSIX"
else
	# Fallback path: without jq we cannot safely merge. If a config already
	# exists, refuse rather than risk discarding the user's other servers, and
	# tell them how to unblock the script.
	if [ -f "$CLAUDE_CFG_POSIX" ]; then
		die "jq is not installed and $CLAUDE_CFG_POSIX already exists.
       Merging safely requires jq (install it via the Cygwin setup, package 'jq'),
       then re-run this script. The config was left untouched."
	fi

	# There is no existing config, so it is safe to write a fresh one. JSON
	# requires each backslash to be doubled; esc() performs that escaping.
	esc() { printf '%s' "$1" | sed 's/\\/\\\\/g'; }
	log "jq not found; writing a fresh Claude Desktop config (no existing servers to preserve)."
	cat > "$CLAUDE_CFG_POSIX" <<EOF
{
  "mcpServers": {
    "$MCP_SERVER_KEY": {
      "command": "$(esc "$EXE_WIN")",
      "args": ["--config", "$(esc "$CONFIG_WIN")"]
    }
  }
}
EOF
fi

# ---------------------------------------------------------------------------
# Done: report what happened and print the one manual step that remains
# ---------------------------------------------------------------------------

# The final block is intentionally verbose: the skill upload is the only step the
# script could not perform, so it spells out exactly what to click and which file
# to choose, and reminds the user to restart Claude Desktop so it picks up the
# newly registered bridge.
cat >&2 <<EOF

Deployment complete.

  Bridge exe      : $EXE_WIN
  Bridge config   : $CONFIG_WIN
  Memory root     : $MEM_WIN  (with blocks\\ and bin\\mcp-bridge\\)
  Claude config   : $CLAUDE_CFG_POSIX  (MCP server '$MCP_SERVER_KEY' registered)
  Skill archive   : $SKILL_STAGED_WIN  (staged; upload it manually — see below)

Two things still need a human:

1. Restart Claude Desktop so it launches the newly registered bridge.

2. Install the memory skill through the Claude Desktop UI (there is no
   file-drop location a script can use):

     Claude Desktop  ->  Customize  ->  Skills
       ->  click "+"  ->  "+ Create skill"  ->  "Upload a skill"
       ->  choose:  $SKILL_STAGED_WIN
       ->  then toggle the skill on.

   Upload the .zip as-is; do not rename it to .skill (Claude Desktop's
   uploader expects a .zip — the .skill extension is only what it produces
   when you *download* an existing skill).

EOF

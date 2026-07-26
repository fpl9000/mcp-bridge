#!/usr/bin/env bash
#
# build-release.sh — Produce a complete, installable set of mcp-bridge deployment
# artifacts in the deploy/ directory.
#
# The installer, deploy/install-bridge.sh, requires four files to sit beside it:
#
#     mcp-bridge.exe   bridge-config.yaml   core.md   stateful-memory.zip
#
# Two of those are tracked sources that already live in deploy/ (install-bridge.sh
# itself and core.md). This script produces the other three, so that after a run
# the deploy/ directory is a self-contained payload that can be copied to the
# Windows machine and installed by running install-bridge.sh from inside it.
#
# What this script does NOT do: install the skill into Claude Desktop. Claude
# Desktop only accepts skills through its UI, so the .zip has to be uploaded by
# hand. That constraint is the reason the skill is packaged here rather than
# being installed here.
#
# Usage:
#
#     ./build-release.sh              # vet, test, build, package
#     ./build-release.sh --no-test    # skip vet and the test suite
#     ./build-release.sh --clean      # remove generated artifacts and exit
#
# Exit status is zero only if every artifact was produced and passed its checks.

# Abort on the first command failure, on any reference to an unset variable, and on a
# failure anywhere in a pipeline. A partially built release must never look successful,
# because the artifacts it leaves behind would be silently stale.
set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Resolve the repository root from this script's own location rather than from the
# caller's working directory, so the script behaves identically no matter where it is
# invoked from.
readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The directory that receives the build output. This is also where the tracked
# installer and the seed core.md already live, which is why the build stages into it
# instead of into a separate dist/ tree: install-bridge.sh resolves its inputs relative
# to itself, so keeping everything in one directory is what makes the payload portable.
readonly DEPLOY_DIR="$REPO_ROOT/deploy"

# The generated artifacts, named exactly as install-bridge.sh expects to find them.
# These names are a contract with the installer, not a preference.
readonly ARTIFACT_EXE="$DEPLOY_DIR/mcp-bridge.exe"
readonly ARTIFACT_SKILL="$DEPLOY_DIR/stateful-memory.zip"
readonly ARTIFACT_CONFIG="$DEPLOY_DIR/bridge-config.yaml"

# The tracked sources the build copies or packages from.
readonly SOURCE_CONFIG="$REPO_ROOT/bridge-config.yaml"
readonly SOURCE_SKILL="$REPO_ROOT/skill/SKILL.md"

# Claude Desktop requires that a skill archive contain a *folder* holding SKILL.md,
# not a bare SKILL.md at the archive root. The folder name must match the skill's
# name: field. Getting this wrong produces a skill that uploads but never loads, so
# the name is fixed here and verified after packaging.
readonly SKILL_DIR_NAME='stateful-memory'

# The cross-compilation target. The bridge always ships as a Windows binary because
# Claude Desktop runs on Fran's Windows machine, even though it is built on Linux.
readonly TARGET_GOOS='windows'
readonly TARGET_GOARCH='amd64'

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

# log prints a progress line to stderr, so that ordinary chatter is never mistaken for
# a value a caller might try to capture from stdout.
log() { printf '  %s\n' "$*" >&2; }

# step prints a section heading, to make a failed run easy to locate in the output.
step() { printf '\n== %s\n' "$*" >&2; }

# die prints an error and exits non-zero. Because "set -e" is in effect most failures
# abort on their own; die is for conditions this script checks explicitly and wants to
# explain in terms the reader can act on.
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# require_tool aborts unless the named external command is on PATH. Checking up front
# means a missing tool is reported before any artifact is written, rather than halfway
# through when some outputs already exist and others do not.
require_tool() {
    command -v "$1" >/dev/null 2>&1 || die "required tool '$1' is not on PATH${2:+ ($2)}"
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

# Whether to run "go vet" and the test suite before building. Tests are on by default:
# the whole point of a release build is that the thing being shipped was verified.
run_tests=1

# Whether this invocation should only delete generated artifacts and stop.
clean_only=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-test|--no-tests)
            run_tests=0
            ;;
        --clean)
            clean_only=1
            ;;
        -h|--help)
            # Reproduce the usage block from the header comment rather than duplicating
            # it, so the two can never disagree.
            sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^#\{1,2\} \{0,1\}//'
            exit 0
            ;;
        *)
            die "unknown argument '$1' (try --help)"
            ;;
    esac
    shift
done

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

# Remove only the three generated artifacts. The tracked files in deploy/
# (install-bridge.sh and core.md) are deliberately left alone: they are sources, and
# deleting them would turn a clean into a data-loss event.
if (( clean_only )); then
    step 'Cleaning generated artifacts'
    rm -f "$ARTIFACT_EXE" "$ARTIFACT_SKILL" "$ARTIFACT_CONFIG"
    log 'Removed the bridge binary, skill archive, and staged config (if present).'
    exit 0
fi

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

step 'Preflight'

require_tool go 'install the Go toolchain from https://go.dev/dl/'
require_tool zip 'needed to package the skill archive'

# Verify the tracked inputs exist before doing any work. A missing source here almost
# always means the script is being run against an incomplete checkout.
[[ -f "$SOURCE_CONFIG" ]] || die "missing tracked source: $SOURCE_CONFIG"
[[ -f "$SOURCE_SKILL"  ]] || die "missing tracked source: $SOURCE_SKILL"
[[ -d "$DEPLOY_DIR"    ]] || die "missing deploy directory: $DEPLOY_DIR"

log "Go toolchain: $(go version)"
log "Repository:   $REPO_ROOT"

# ---------------------------------------------------------------------------
# Vet and test
# ---------------------------------------------------------------------------

# Vet and test run natively (for the host platform), not cross-compiled, because Go
# cannot execute Windows test binaries on Linux. The code has no build tags that vary
# by platform and no cgo, so a native test run is a valid check of the same source that
# the Windows binary is compiled from.
if (( run_tests )); then
    step 'Vetting and testing (native)'
    cd "$REPO_ROOT"
    go vet ./...
    go test ./...
else
    step 'Skipping vet and tests (--no-test)'
fi

# ---------------------------------------------------------------------------
# Build the bridge binary
# ---------------------------------------------------------------------------

step "Building $TARGET_GOOS/$TARGET_GOARCH binary"

cd "$REPO_ROOT"

# CGO is disabled so the result is a single statically linked executable with no DLL
# dependencies, which is what makes the deployment a plain file copy.
#
# Note the absence of -ldflags "-H windowsgui": the bridge is a stdio MCP server, and
# that flag would detach it from its console and break the transport it speaks over.
CGO_ENABLED=0 GOOS="$TARGET_GOOS" GOARCH="$TARGET_GOARCH" \
    go build -trimpath -o "$ARTIFACT_EXE" .

log "Built $(basename "$ARTIFACT_EXE")"

# Confirm the binary really is a Windows executable. Cross-compilation failures tend to
# be silent in the sense that a Linux ELF binary named ".exe" looks fine to a casual
# glance, and would only fail once it reached the Windows machine.
if command -v file >/dev/null 2>&1; then
    exe_type="$(file -b "$ARTIFACT_EXE")"
    case "$exe_type" in
        *'PE32+'*) log "Verified format: $exe_type" ;;
        *)         die "built binary is not a Windows PE32+ executable: $exe_type" ;;
    esac
else
    log 'Skipping binary format check ("file" is not installed).'
fi

# ---------------------------------------------------------------------------
# Package the skill archive
# ---------------------------------------------------------------------------

step 'Packaging the skill archive'

# Build the archive in a temporary directory so that the folder structure inside the
# .zip comes from a directory that exists only for this purpose. Packaging in place
# would require creating a stateful-memory/ folder inside the repo, which would then
# have to be gitignored and could drift.
skill_staging="$(mktemp -d)"

# Remove the staging directory on exit no matter how the script terminates, so a failed
# run does not litter /tmp.
trap 'rm -rf "$skill_staging"' EXIT

mkdir -p "$skill_staging/$SKILL_DIR_NAME"
cp "$SOURCE_SKILL" "$skill_staging/$SKILL_DIR_NAME/SKILL.md"

# Delete any previous archive first. The zip command adds to an existing archive rather
# than replacing it, so without this a renamed or removed file would linger forever in
# the .zip across builds.
rm -f "$ARTIFACT_SKILL"

# -r recurses into the skill folder; -q keeps the output quiet; -X omits extra file
# attributes (uid/gid, host OS metadata) that vary between build machines and would
# otherwise make two builds of identical content produce different archives.
( cd "$skill_staging" && zip -r -q -X "$ARTIFACT_SKILL" "$SKILL_DIR_NAME" )

log "Built $(basename "$ARTIFACT_SKILL")"

# Verify the archive has the folder structure Claude Desktop requires. This check
# exists because the opposite layout — a bare SKILL.md at the archive root — was a real
# defect: it uploads without complaint and then silently fails to load.
if command -v unzip >/dev/null 2>&1; then
    archive_listing="$(unzip -Z1 "$ARTIFACT_SKILL")"
    if ! grep -qx "$SKILL_DIR_NAME/SKILL.md" <<<"$archive_listing"; then
        die "skill archive lacks $SKILL_DIR_NAME/SKILL.md; contents are: $archive_listing"
    fi
    log "Verified archive layout: $SKILL_DIR_NAME/SKILL.md"
else
    log 'Skipping archive layout check ("unzip" is not installed).'
fi

# Verify the skill has YAML frontmatter with a name: field. A skill without frontmatter
# uploads successfully and is then never indexed or invoked, which is a confusing
# failure to diagnose from the Claude Desktop side.
if ! head -1 "$SOURCE_SKILL" | grep -qx -- '---'; then
    die "$SOURCE_SKILL does not begin with YAML frontmatter ('---' on line 1)"
fi
if ! sed -n '2,10p' "$SOURCE_SKILL" | grep -q '^name:'; then
    die "$SOURCE_SKILL frontmatter has no name: field"
fi
log 'Verified skill frontmatter (name: field present).'

# ---------------------------------------------------------------------------
# Stage the bridge configuration
# ---------------------------------------------------------------------------

step 'Staging the bridge configuration'

# The canonical bridge-config.yaml lives at the repository root; the installer wants a
# copy beside itself. Copying rather than symlinking keeps the deploy/ directory
# meaningful after it is archived or moved to another machine.
cp "$SOURCE_CONFIG" "$ARTIFACT_CONFIG"
log "Staged $(basename "$ARTIFACT_CONFIG")"

# ---------------------------------------------------------------------------
# Final verification and summary
# ---------------------------------------------------------------------------

step 'Release payload'

# Re-check all four installer inputs together, including the two tracked files this
# script does not generate. The point is to answer the question the user actually has
# — "is deploy/ ready to copy to the Windows box?" — rather than only reporting on the
# steps that just ran.
missing=0
for artifact in mcp-bridge.exe bridge-config.yaml core.md stateful-memory.zip; do
    path="$DEPLOY_DIR/$artifact"
    if [[ -f "$path" ]]; then
        # Report each artifact with its size, so an obviously truncated or empty file
        # is visible in the summary without a separate inspection step.
        printf '  %-22s %8s bytes\n' "$artifact" "$(wc -c <"$path")" >&2
    else
        printf '  %-22s MISSING\n' "$artifact" >&2
        missing=1
    fi
done

(( missing == 0 )) || die 'the release payload is incomplete (see MISSING above)'

log ''
log "Payload is complete in: $DEPLOY_DIR"
log 'Copy that directory to the Windows machine and run install-bridge.sh from'
log 'inside it. The skill archive still has to be uploaded through the Claude'
log 'Desktop UI by hand; install-bridge.sh prints those steps when it finishes.'

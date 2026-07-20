// frontmatter.go implements YAML frontmatter parsing and composition for
// block files, plus the atomic write procedure shared by every piece of
// bridge state that touches disk (blocks, core, and the persisted bridge
// state), per design spec Section 3.16.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// frontmatterDelimiter is the line that opens and closes a block file's YAML
// header, matching the on-disk format shown in Section 3.16.
const frontmatterDelimiter = "---"

// BlockFrontmatter is the YAML metadata header on every block file. Core
// (core.md) has no frontmatter and never uses this type — see Section 4.3.
type BlockFrontmatter struct {
	Summary   string    `yaml:"summary"`
	UpdatedAt time.Time `yaml:"updated_at"`
}

// splitFrontmatter separates a block file's raw bytes into parsed frontmatter
// and the body that follows it. If the content has no recognizable
// frontmatter header — e.g. a block the user hand-created or hand-edited
// without preserving the header — hasFrontmatter is false and the entire
// content is returned as the body. This lets the bridge tolerate damaged
// frontmatter rather than refusing to read the file (Section 3.16's crash
// recovery notes).
func splitFrontmatter(raw []byte) (fm BlockFrontmatter, body string, hasFrontmatter bool) {
	text := string(raw)

	if !strings.HasPrefix(text, frontmatterDelimiter+"\n") {
		return BlockFrontmatter{}, text, false
	}

	afterOpen := text[len(frontmatterDelimiter)+1:]
	closeMarker := "\n" + frontmatterDelimiter
	closeIndex := strings.Index(afterOpen, closeMarker)
	if closeIndex == -1 {
		return BlockFrontmatter{}, text, false
	}

	yamlPart := afterOpen[:closeIndex]

	rest := afterOpen[closeIndex+len(closeMarker):]
	// The closing delimiter is followed by its own newline, then (per the
	// Section 3.16 example) a blank separator line before the body. Both are
	// stripped if present, but their absence is tolerated.
	rest = strings.TrimPrefix(rest, "\n")
	rest = strings.TrimPrefix(rest, "\n")

	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return BlockFrontmatter{}, text, false
	}

	return fm, rest, true
}

// composeBlockFile assembles a block file's on-disk bytes from frontmatter
// and body. The frontmatter and body are written together as one value, so —
// unlike the v1 design's separate index file — they can never diverge.
func composeBlockFile(fm BlockFrontmatter, body string) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(frontmatterDelimiter)
	buf.WriteString("\n")
	buf.Write(yamlBytes)
	buf.WriteString(frontmatterDelimiter)
	buf.WriteString("\n\n")
	buf.WriteString(body)

	return buf.Bytes(), nil
}

// deriveDefaultSummary produces a placeholder summary for a block file whose
// frontmatter is missing or damaged, per Section 3.16: "The default summary
// is derived mechanically (e.g., the first heading or first line, truncated)."
func deriveDefaultSummary(body string, maxLength int) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		trimmed = strings.TrimLeft(trimmed, "# ")
		if len(trimmed) > maxLength {
			trimmed = trimmed[:maxLength]
		}

		return trimmed
	}

	return ""
}

// atomicWriteFile writes data to a temp file in path's directory, syncs it,
// and renames it over path — the temp-and-rename sequence from Section 3.16
// so a reader never observes a partial file and a failed write leaves no
// corruption. The temp file is removed on any failure along the way.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()

	cleanup := func() {
		tempFile.Close()
		os.Remove(tempPath)
	}

	if _, err := tempFile.Write(data); err != nil {
		cleanup()
		return err
	}

	if err := tempFile.Sync(); err != nil {
		cleanup()
		return err
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}

	if err := os.Chmod(tempPath, perm); err != nil {
		os.Remove(tempPath)
		return err
	}

	// os.Rename cannot atomically overwrite an existing file on all Windows
	// filesystems (Section 3.16's "Windows rename caveat"), so the target is
	// removed first. The resulting non-existence window is acceptably tiny
	// given mutex-serialized, low-frequency memory writes.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		os.Remove(tempPath)
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}

	return nil
}

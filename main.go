// main.go is the bridge's entry point: it loads configuration, loads
// persisted state, registers the memory-aware tools, starts the MCP stdio
// server, and persists state on shutdown, per design spec Sections 3.1 and
// 3.24.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// version is reported during the MCP initialize handshake and by --version.
const version = "0.1.0"

// Bridge bundles the shared state every memory tool handler needs. A single
// instance is constructed in main and its handler methods (defined across
// handles.go, memory_core.go, memory_index.go, memory_block.go, and
// memory_episodic.go) are registered as MCP tool callbacks in tools.go.
type Bridge struct {
	Config     *Config
	Logger     *Logger
	Handles    *HandleMap
	Mutex      *MemMutex
	Persist    *Persistence
	IndexCache *IndexCache
}

func main() {
	versionFlag := flag.Bool("version", false, "print the bridge version and exit")
	configFlag := flag.String("config", "", "path to bridge-config.yaml (overrides MCP_BRIDGE_CONFIG and the default location)")
	flag.Parse()

	if *versionFlag {
		fmt.Println("mcp-bridge " + version)
		return
	}

	configPath := ResolveConfigPath(*configFlag)
	cfg, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: config error: %v\n", err)
		os.Exit(1)
	}

	logger, err := NewLogger(cfg.Logging.File, cfg.Logging.Level)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: logging error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	// The full design's branching subsystem is not implemented in this
	// build (IMPLEMENTATION-PROMPT.md Section 5): warn rather than silently
	// implying behavior that isn't there.
	if cfg.Branching.Enabled {
		logger.Warn("branching not implemented in this build; last-writer-wins in effect", nil)
	}

	sweepOrphanTempFiles(cfg, logger)

	handleMap := NewHandleMap(cfg.Handle.IDLength)
	persist, recovered := NewPersistence(cfg, handleMap, logger)

	bridge := &Bridge{
		Config:     cfg,
		Logger:     logger,
		Handles:    handleMap,
		Mutex:      &MemMutex{},
		Persist:    persist,
		IndexCache: NewIndexCache(),
	}

	logger.Info("bridge started", map[string]any{
		"config_path":       configPath,
		"version":           version,
		"handles_recovered": recovered,
	})

	s := server.NewMCPServer(
		"mcp-bridge",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	RegisterTools(s, bridge)

	// ServeStdio blocks until stdin reaches EOF — the authoritative shutdown
	// signal for a stdio MCP server on Windows (Section 3.24).
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: ServeStdio error: %v\n", err)
	}

	// Stdin EOF received — begin graceful shutdown. There are no
	// subprocesses to kill in this build (spawn_agent and run_command are
	// out of scope), so the only shutdown duty is the final state write.
	if checkpointErr := bridge.Persist.WriteCheckpoint(); checkpointErr != nil {
		logger.Error("final state checkpoint failed", map[string]any{"error": checkpointErr.Error()})
	} else {
		logger.Info("bridge shutdown: final state checkpoint written", nil)
	}
}

// sweepOrphanTempFiles removes leftover "*.tmp.*" files from an interrupted
// atomic write, per Section 3.16's crash-recovery startup sweeper. Only
// files older than a few seconds are removed, so this can never race with a
// write that is still genuinely in flight.
func sweepOrphanTempFiles(cfg *Config, logger *Logger) {
	const orphanAge = 5 * time.Second
	cutoff := time.Now().Add(-orphanAge)

	for _, dir := range []string{cfg.Memory.Directory, cfg.BlocksDirectory()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.Contains(entry.Name(), ".tmp.") {
				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil || info.ModTime().After(cutoff) {
				continue
			}

			path := filepath.Join(dir, entry.Name())
			if removeErr := os.Remove(path); removeErr == nil {
				logger.Info("removed orphan temp file", map[string]any{"path": path})
			}
		}
	}
}

// config.go loads and validates the bridge's YAML configuration, per design
// spec Section 3.2. The full schema is parsed so the config file never has
// to change shape when the deferred features (sub-agent spawning, run_command,
// branching, maintenance) land in a later build; only the sections this build
// actually implements are validated.
package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// defaultConfigYAML is the shipped default configuration, embedded into the
// binary so the bridge can start with sane values even when no external
// config file exists yet at the resolved path.
//
//go:embed bridge-config.yaml
var defaultConfigYAML []byte

// defaultConfigPath is the fallback location when neither --config nor
// MCP_BRIDGE_CONFIG is supplied. It matches the Windows path Claude Desktop
// uses on the shippable target platform (Section 3.2).
const defaultConfigPath = `C:\franl\.claude-agent-memory\bridge-config.yaml`

// AsyncConfig holds settings for the (deferred) shared async executor.
type AsyncConfig struct {
	SyncWindowSeconds int `yaml:"sync_window_seconds"`
	JobExpirySeconds  int `yaml:"job_expiry_seconds"`
}

// SubAgentConfig holds defaults for the (deferred) spawn_agent tool.
type SubAgentConfig struct {
	DefaultTimeoutSeconds  int `yaml:"default_timeout_seconds"`
	DefaultMaxOutputTokens int `yaml:"default_max_output_tokens"`
	MaxConcurrentAgents    int `yaml:"max_concurrent_agents"`
}

// RunCommandConfig holds settings for the (deferred) run_command tool.
type RunCommandConfig struct {
	Shell                 string   `yaml:"shell"`
	ShellArgs             []string `yaml:"shell_args"`
	DefaultTimeoutSeconds int      `yaml:"default_timeout_seconds"`
	DefaultMaxOutputBytes int      `yaml:"default_max_output_bytes"`
}

// MemoryConfig holds the memory root directory and per-block limits. This
// section is validated and enforced by this build.
type MemoryConfig struct {
	Directory        string `yaml:"directory"`
	SummaryMaxLength int    `yaml:"summary_max_length"`

	// AlwaysLoadBlocks names blocks whose content memory_start_conversation
	// returns alongside core, without the caller having to ask for them.
	//
	// It exists for content the LLM must apply to the act of writing rather
	// than consult about a topic -- formatting conventions being the
	// motivating case. The index lets a caller find blocks *by topic*, which
	// cannot surface a block that is relevant to every conversation and to no
	// particular opening message. Naming such a block here removes the need
	// for the caller to think to look for it.
	//
	// Empty by default, and deliberately so: a fresh installation has no
	// blocks at all, so any non-empty default would name something that does
	// not exist. This is per-installation configuration rather than something
	// the shipped skill can state, which keeps the skill free of references
	// to blocks that exist only in one person's memory store.
	AlwaysLoadBlocks []string `yaml:"always_load_blocks"`

	// AlwaysLoadMaxBytes caps the total body size of always-loaded blocks.
	// Their cost is paid on every conversation whether or not they are used,
	// so an unbounded list would quietly consume context. On exceeding the
	// cap the bridge loads what fits, in configured order, and reports the
	// omission rather than silently truncating. Zero or negative disables
	// the cap.
	AlwaysLoadMaxBytes int `yaml:"always_load_max_bytes"`
}

// HandleConfig controls handle minting and (deferred) eviction.
type HandleConfig struct {
	IDLength      int `yaml:"id_length"`
	RetentionDays int `yaml:"retention_days"`
}

// PersistenceConfig controls the .bridge-state.json checkpoint behavior.
type PersistenceConfig struct {
	StateFile                 string `yaml:"state_file"`
	CheckpointIntervalSeconds int    `yaml:"checkpoint_interval_seconds"`
}

// BranchingConfig is the debugging escape hatch from the full design. This
// build never branches regardless of this value; see main.go for the startup
// warning logged when it is true.
type BranchingConfig struct {
	Enabled bool `yaml:"enabled"`
}

// MaintenanceConfig holds settings for the (deferred) memory_run_maintenance
// tool.
type MaintenanceConfig struct {
	MaxBlocksPerCall int `yaml:"max_blocks_per_call"`
}

// LoggingConfig controls the structured log file. This section is validated
// and enforced by this build.
type LoggingConfig struct {
	File       string `yaml:"file"`
	Level      string `yaml:"level"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups"`
}

// ClaudeCLIConfig holds the (deferred) Claude Code CLI path used by
// spawn_agent and the maintenance merge sub-agents.
type ClaudeCLIConfig struct {
	Path string `yaml:"path"`
}

// Config is the full bridge configuration, matching the schema in design
// spec Section 3.2.
type Config struct {
	Async                   AsyncConfig       `yaml:"async"`
	DefaultWorkingDirectory string            `yaml:"default_working_directory"`
	SubAgent                SubAgentConfig    `yaml:"sub_agent"`
	RunCommand              RunCommandConfig  `yaml:"run_command"`
	Memory                  MemoryConfig      `yaml:"memory"`
	Handle                  HandleConfig      `yaml:"handle"`
	Persistence             PersistenceConfig `yaml:"persistence"`
	Branching               BranchingConfig   `yaml:"branching"`
	Maintenance             MaintenanceConfig `yaml:"maintenance"`
	Logging                 LoggingConfig     `yaml:"logging"`
	ClaudeCLI               ClaudeCLIConfig   `yaml:"claude_cli"`
}

// ResolveConfigPath implements the location priority order from Section 3.2:
// the --config flag wins, then MCP_BRIDGE_CONFIG, then the built-in default.
func ResolveConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	if envValue := os.Getenv("MCP_BRIDGE_CONFIG"); envValue != "" {
		return envValue
	}

	return defaultConfigPath
}

// LoadConfig reads the YAML file at path, falling back to the embedded
// default when no file exists there yet, then validates the in-scope subset
// of the schema. A best-effort attempt writes the embedded default out to
// path so the user has a starting file to edit; failure to write (e.g. a
// read-only or nonexistent parent on this platform) is not fatal — the
// embedded bytes are parsed directly in that case.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading config file %q: %w", path, err)
		}

		// No config file at the resolved location: seed one from the
		// embedded default (best-effort) and fall back to parsing the
		// embedded bytes directly regardless of whether the seed succeeded.
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr == nil {
			_ = os.WriteFile(path, defaultConfigYAML, 0o644)
		}
		raw = defaultConfigYAML
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// validate checks only the sections this build actually implements: memory,
// handle, persistence, and logging. Per the implementation prompt, the
// executability of claude_cli.path and run_command.shell is not checked, and
// async.sync_window_seconds < 30 is not enforced — those guard deferred
// features and enforcing them would stop the bridge from starting for no
// reason.
func (c *Config) validate() error {
	if c.Memory.Directory == "" {
		return errors.New("memory.directory must be set")
	}

	if c.Memory.SummaryMaxLength <= 0 {
		return errors.New("memory.summary_max_length must be > 0")
	}

	// Reject block names that could never resolve. A name that fails
	// isValidBlockName is a typo or a path-traversal attempt, and either way
	// silently skipping it at load time would hide the mistake for as long as
	// the operator kept believing the block was being loaded. A name that is
	// merely absent from disk is NOT rejected here: a fresh installation has
	// no blocks yet, and a block named here may legitimately be created later.
	for _, name := range c.Memory.AlwaysLoadBlocks {
		if !isValidBlockName(name) {
			return fmt.Errorf("memory.always_load_blocks contains invalid block name %q; use letters, digits, hyphens, and underscores", name)
		}
	}

	if c.Handle.IDLength < 8 {
		return errors.New("handle.id_length must be >= 8")
	}

	if c.Handle.RetentionDays <= 0 {
		return errors.New("handle.retention_days must be > 0")
	}

	if c.Persistence.CheckpointIntervalSeconds < 1 {
		return errors.New("persistence.checkpoint_interval_seconds must be >= 1")
	}

	if c.Logging.File == "" {
		return errors.New("logging.file must be set")
	}

	// Create the memory directory and its blocks/ subdirectory if they
	// don't exist yet, per Section 3.2's validator description.
	blocksDir := filepath.Join(c.Memory.Directory, "blocks")
	if err := os.MkdirAll(blocksDir, 0o755); err != nil {
		return fmt.Errorf("creating memory directory %q: %w", blocksDir, err)
	}

	// Create the log file's parent directory if it doesn't exist yet.
	if err := os.MkdirAll(filepath.Dir(c.Logging.File), 0o755); err != nil {
		return fmt.Errorf("creating log directory for %q: %w", c.Logging.File, err)
	}

	return nil
}

// BlocksDirectory returns the path to the blocks/ subdirectory under the
// configured memory root.
func (c *Config) BlocksDirectory() string {
	return filepath.Join(c.Memory.Directory, "blocks")
}

// CorePath returns the path to core.md under the configured memory root.
func (c *Config) CorePath() string {
	return filepath.Join(c.Memory.Directory, "core.md")
}

// StateFilePath returns the absolute path to the persisted bridge state file,
// which is stored relative to memory.directory per Section 3.2.
func (c *Config) StateFilePath() string {
	return filepath.Join(c.Memory.Directory, c.Persistence.StateFile)
}

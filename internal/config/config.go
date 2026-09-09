// Package config owns the tenzing.yaml schema: one file for every durable
// setting — models (registry included; this file replaces models.yaml),
// advisor, subagent, budgets, permissions, serve settings. Parsing is
// strict: unknown keys are a startup error so typos fail loudly.
//
// Precedence is applied by cmd/app, not here: CLI flag > env var >
// tenzing.yaml > default. Scalars whose zero value is a meaningful setting
// (subagent_depth, approval_timeout, thinking, port) are pointers so the
// merge can tell "set to zero" from "omitted". $VAR / ${VAR} references are
// expanded from the environment before parsing.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// File is the on-disk shape of tenzing.yaml. All keys are optional.
type File struct {
	// Model refs are "provider/name" or inline {provider, name, ...} JSON/YAML.
	Model           string `yaml:"model"`
	SubagentModel   string `yaml:"subagent_model"`
	BlackboardModel string `yaml:"blackboard_model"`
	// AdvisorModel enables the advisor tool and its write-gate when set.
	AdvisorModel string `yaml:"advisor_model"`
	AdvisorNudge int    `yaml:"advisor_nudge"`

	MaxTokens     int64     `yaml:"max_tokens"`
	MaxIterations int       `yaml:"max_iterations"`
	MaxWallClock  *Duration `yaml:"max_wall_clock"`

	SubagentDepth   *int      `yaml:"subagent_depth"`
	ApprovalTimeout *Duration `yaml:"approval_timeout"`
	NoPermissions   bool      `yaml:"no_permissions"`
	SkipPermissions bool      `yaml:"dangerously_skip_permissions"`
	ReadOnly        bool      `yaml:"read_only"`
	Thinking        *bool     `yaml:"thinking"`
	NoSession       bool      `yaml:"no_session"`
	NoContextFiles  bool      `yaml:"no_context_files"`

	SystemFile string `yaml:"system_file"`
	BaseURL    string `yaml:"base_url"`
	// APIKey works but env vars / --api-key are preferred over a key on disk.
	APIKey string `yaml:"api_key"`

	Port        *int   `yaml:"port"`
	NexusConfig string `yaml:"nexus_config"`
	Debug       bool   `yaml:"debug"`

	MCPServers []MCPServer `yaml:"mcp_servers"`

	// Permissions overrides the default tool permission policy per tool
	// name. Nil means the default policy is used unchanged.
	Permissions *PermissionsSection `yaml:"permissions"`

	Models ModelsSection `yaml:"models"`
}

// PermissionsSection overrides the default permission policy. A tool named
// in one list is removed from the other two, so `allow: [Write, Edit]` is
// enough to stop those prompts while bash and MCP tools keep asking. Names
// match case-insensitively. AskOrigins, when non-empty, replaces the default
// origin prefixes ("mcp:") entirely.
type PermissionsSection struct {
	Allow      []string `yaml:"allow"`
	Deny       []string `yaml:"deny"`
	Ask        []string `yaml:"ask"`
	AskOrigins []string `yaml:"ask_origins"`
}

// MCPServer mounts one MCP server (structured form of --mcp-server's
// "name=command arg1 arg2").
type MCPServer struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

// ModelsSection is the model registry: what models.yaml used to hold.
// Default is the model used when neither --model, TENZING_MODEL, nor the
// top-level `model:` key picks one.
type ModelsSection struct {
	Default string       `yaml:"default"`
	Entries []ModelEntry `yaml:"entries"`
}

// ModelEntry defines a custom model. ContextWindow and MaxTokens are
// defaulted by the registry when omitted; BaseURL applies to the whole
// provider.
type ModelEntry struct {
	Provider      string     `yaml:"provider"`
	Name          string     `yaml:"name"`
	ContextWindow int        `yaml:"context_window"`
	MaxTokens     int        `yaml:"max_tokens"`
	BaseURL       string     `yaml:"base_url"`
	Cost          *CostEntry `yaml:"cost"`
	// Vision marks the model as accepting image input; image-bearing
	// queries are rejected on models without it.
	Vision bool `yaml:"vision"`
}

// CostEntry is USD per million tokens. CacheRead/CacheWrite default to the
// Anthropic convention (0.1x / 1.25x the input rate) where consumed.
type CostEntry struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// Duration is a time.Duration that unmarshals from Go duration strings
// ("90s", "5m") or bare integers (nanoseconds, yaml's native int).
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("want a duration string like \"90s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Value returns the underlying time.Duration; zero when d is nil.
func (d *Duration) Value() time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(*d)
}

// Load reads and strictly parses a tenzing.yaml. A missing file is an error
// when explicit (the path came from --config or TENZING_CONFIG) and a clean
// found=false otherwise (the ambient default path just isn't there).
func Load(path string, explicit bool) (File, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("read config %s: %w", path, err)
	}

	var f File
	dec := yaml.NewDecoder(strings.NewReader(expandEnv(string(data))))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		// An empty file decodes to EOF; treat it as an all-defaults config.
		if errors.Is(err, io.EOF) {
			return File{}, true, nil
		}
		if strings.Contains(err.Error(), "field default not found") {
			return File{}, false, fmt.Errorf("parse config %s: %w\n(this looks like an old models.yaml — nest it under a models: key, with the model list renamed to entries:)", path, err)
		}
		return File{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}

	if err := f.validate(); err != nil {
		return File{}, false, fmt.Errorf("config %s: %w", path, err)
	}
	return f, true, nil
}

// expandEnv substitutes $VAR / ${VAR} from the environment across the whole
// file, so secrets can live in the environment and be referenced from disk
// (api_key: "$OLLAMA_API_KEY"). References to unset variables are left as
// written rather than blanked, so a typo is visible instead of silent.
func expandEnv(s string) string {
	return os.Expand(s, func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return "$" + name
	})
}

func (f File) validate() error {
	for i, s := range f.MCPServers {
		if s.Name == "" || s.Command == "" {
			return fmt.Errorf("mcp_servers[%d]: name and command are required", i)
		}
	}
	for i, e := range f.Models.Entries {
		if e.Provider == "" || e.Name == "" {
			return fmt.Errorf("models.entries[%d]: provider and name are required", i)
		}
	}
	if f.AdvisorNudge < 0 {
		return fmt.Errorf("advisor_nudge must be >= 0, got %d", f.AdvisorNudge)
	}
	return nil
}

// Package config owns the tenzing.yaml schema: one file for every durable
// setting — providers and models (the registry lives here; this file
// replaces models.yaml), advisor, subagent, budgets, permissions, serve
// settings. Parsing is strict: unknown keys are a startup error so typos
// fail loudly.
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
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// File is the on-disk shape of tenzing.yaml. All keys are optional.
type File struct {
	// Model refs name an entry in Models by its `name`, or are an inline
	// {provider, model_name, ...} JSON/YAML definition naming a declared
	// provider. Model is required: it is the main agent's model, and the
	// fallback for every role that leaves its own ref empty.
	Model           string `yaml:"model"`
	SubagentModel   string `yaml:"subagent_model"`
	BlackboardModel string `yaml:"blackboard_model"`
	// AdvisorModel enables the advisor tool and its write-gate when set.
	AdvisorModel string `yaml:"advisor_model"`
	AdvisorNudge int    `yaml:"advisor_nudge"`

	// MaxTurnTokens bounds input+output cumulatively for one turn; a
	// model entry's MaxResponseTokens bounds a single response instead.
	MaxTurnTokens int64     `yaml:"max_turn_tokens"`
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

	Port        *int   `yaml:"port"`
	NexusConfig string `yaml:"nexus_config"`
	Debug       bool   `yaml:"debug"`

	MCPServers []MCPServer `yaml:"mcp_servers"`

	// Permissions overrides the default tool permission policy per tool
	// name. Nil means the default policy is used unchanged.
	Permissions *PermissionsSection `yaml:"permissions"`

	// Providers are the backends models are served from; Models are the
	// named model definitions that reference them. Both are required for
	// any model to resolve — there is no compiled-in fallback set.
	Providers []Provider   `yaml:"providers"`
	Models    []ModelEntry `yaml:"models"`
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

// ProviderTypes are the wire protocols a provider can speak — the three
// the repo actually implements, not a list of vendors. Most hosted APIs
// speak the OpenAI protocol, so DefaultProviderType covers them all and a
// provider entry usually needs no type at all; what distinguishes one such
// backend from another is its URL, not its name.
var ProviderTypes = []string{"anthropic", "ollama", DefaultProviderType}

// DefaultProviderType is the type a provider gets when it names none.
const DefaultProviderType = "openai_compat"

// Provider is one backend models are served from. Name is a unique label
// referenced by ModelEntry.Provider, and is what the backend reports itself
// as in logs. Type defaults to DefaultProviderType. URL is required for
// openai_compat — there is nothing to default to, since the URL is the only
// thing identifying which backend it is — and optional for anthropic and
// ollama, whose clients carry their vendor's endpoint. APIKey is optional
// (omitted means the endpoint needs no auth, as with a local Ollama) and is
// usually written as "$SOME_VAR" so the secret stays in the environment;
// see expandEnv.
//
// Extra injects fields into every request body, by dotted path
// ("provider.sort: throughput"). It is an openai_compat concept and is
// ignored on the other types. One key is reserved: max_completion_tokens
// renames the token-limit parameter rather than adding a field, which is
// what current OpenAI models require.
type Provider struct {
	Name   string         `yaml:"name"`
	Type   string         `yaml:"type"`
	URL    string         `yaml:"url"`
	APIKey string         `yaml:"api_key"`
	Extra  map[string]any `yaml:"extra"`
}

// ModelEntry defines a model. Name is the unique local alias that model
// refs use; ModelName is the id sent to the provider on the wire (the two
// differ so a config can call glm-5.3-flash "main-model"). Provider names
// an entry in File.Providers. ContextWindow and MaxResponseTokens are
// defaulted by the registry when omitted.
type ModelEntry struct {
	Provider      string `yaml:"provider"`
	Name          string `yaml:"name"`
	ModelName     string `yaml:"model_name"`
	ContextWindow int    `yaml:"context_window"`
	// MaxResponseTokens is the cap on a single response; the file's
	// top-level MaxTurnTokens bounds a whole turn instead.
	MaxResponseTokens int        `yaml:"max_response_tokens"`
	Cost              *CostEntry `yaml:"cost"`
	// Vision marks the model as accepting image input; image-bearing
	// queries are rejected on models without it.
	Vision bool `yaml:"vision"`
	// ReasoningEffort sets the provider's reasoning tier for this model,
	// sent verbatim ("low"/"medium"/"high", plus "max" on Ollama). The
	// provider validates it; a bad value fails the first request, not
	// startup. Ignored by providers with no tier concept (Anthropic).
	ReasoningEffort string `yaml:"reasoning_effort"`
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

// ValidateProviders checks a provider list for the required fields, a known
// type, and unique names. Exported because cmd/app assembles the effective
// list from the file plus --provider flags and needs the same checks after
// merging, not just at parse time.
func ValidateProviders(ps []Provider) error {
	seen := map[string]bool{}
	for i := range ps {
		p := &ps[i]
		// An absent type is the common case: most backends speak the
		// OpenAI protocol. Filled in here so nothing downstream has to
		// re-apply the default.
		if p.Type == "" {
			p.Type = DefaultProviderType
		}
		switch {
		case p.Name == "":
			return fmt.Errorf("providers[%d]: name is required", i)
		case !slices.Contains(ProviderTypes, p.Type):
			return fmt.Errorf("providers[%d] (%s): unknown type %q (one of: %s; omit it for %s)",
				i, p.Name, p.Type, strings.Join(ProviderTypes, ", "), DefaultProviderType)
		// Only openai_compat needs a URL: for anthropic and ollama the
		// client knows its vendor's endpoint.
		case p.Type == DefaultProviderType && p.URL == "":
			return fmt.Errorf("providers[%d] (%s): url is required for %s providers", i, p.Name, DefaultProviderType)
		case seen[p.Name]:
			return fmt.Errorf("providers[%d]: duplicate provider name %q", i, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func (f File) validate() error {
	for i, s := range f.MCPServers {
		if s.Name == "" || s.Command == "" {
			return fmt.Errorf("mcp_servers[%d]: name and command are required", i)
		}
	}
	if err := ValidateProviders(f.Providers); err != nil {
		return err
	}
	providers := map[string]bool{}
	for _, p := range f.Providers {
		providers[p.Name] = true
	}

	models := map[string]bool{}
	for i, e := range f.Models {
		switch {
		case e.Name == "":
			return fmt.Errorf("models[%d]: name is required", i)
		case e.ModelName == "":
			return fmt.Errorf("models[%d] (%s): model_name is required", i, e.Name)
		case e.Provider == "":
			return fmt.Errorf("models[%d] (%s): provider is required", i, e.Name)
		case !providers[e.Provider]:
			return fmt.Errorf("models[%d] (%s): provider %q is not declared in providers:", i, e.Name, e.Provider)
		case models[e.Name]:
			return fmt.Errorf("models[%d]: duplicate model name %q", i, e.Name)
		}
		models[e.Name] = true
	}

	if f.AdvisorNudge < 0 {
		return fmt.Errorf("advisor_nudge must be >= 0, got %d", f.AdvisorNudge)
	}
	return nil
}

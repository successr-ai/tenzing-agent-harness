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
	// AdvisorCadence is the max loop iterations between advisor consults
	// before every tool is blocked; 0 = default (6), -1 = off.
	AdvisorCadence int `yaml:"advisor_cadence"`
	// AdvisorMaxCalls caps advisor consults per turn; 0 = default (30).
	AdvisorMaxCalls int `yaml:"advisor_max_calls"`

	// SystemOneModel names a models.systemone: entry and turns on the
	// decision-model consumers in internal/features/systemone. Unlike the
	// refs above it is an alias only — a System One model is declared, not
	// written inline. SystemOne tunes those consumers and does nothing
	// without it.
	SystemOneModel string            `yaml:"systemone_model"`
	SystemOne      *SystemOneSection `yaml:"systemone"`

	// MaxTurnTokens bounds input+output cumulatively for one turn; a
	// model entry's MaxResponseTokens bounds a single response instead.
	MaxTurnTokens int64     `yaml:"max_turn_tokens"`
	MaxIterations int       `yaml:"max_iterations"`
	MaxWallClock  *Duration `yaml:"max_wall_clock"`
	// ThinkingBudget caps reasoning tokens per LLM call for the main agent
	// (exact on Anthropic, tiered on OpenAI-compatible, think level on
	// Ollama). Nil leaves the provider default.
	ThinkingBudget *int64 `yaml:"thinking_budget"`

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

	// Connect configures --connect mode: the process dials the control
	// plane's WebSocket server instead of listening. URL is required for
	// the mode; Token and Backoff are optional.
	Connect *ConnectSection `yaml:"connect"`

	MCPServers []MCPServer `yaml:"mcp_servers"`

	// Permissions overrides the default tool permission policy per tool
	// name. Nil means the default policy is used unchanged.
	Permissions *PermissionsSection `yaml:"permissions"`

	// Providers are the backends models are served from; Models are the
	// named model definitions that reference them. Both are required for
	// any model to resolve — there is no compiled-in fallback set.
	Providers []Provider    `yaml:"providers"`
	Models    ModelsSection `yaml:"models"`
}

// ModelsSection is tenzing.yaml's models: block, split by the kind of model
// declared: llm: entries are chat models behind common.LLM, systemone:
// entries are decision models behind common.SystemOne. The two kinds share
// one alias namespace, because refs (model:, --model, advisor_model:) name a
// bare alias and would otherwise be ambiguous.
//
// A bare sequence is still accepted and loads as LLM, so configs written
// before the split keep working:
//
//	models:            models:
//	  - name: main       llm:
//	    ...                - name: main
//	                         ...
type ModelsSection struct {
	LLM       []ModelEntry     `yaml:"llm"`
	SystemOne []SystemOneEntry `yaml:"systemone"`
}

// UnmarshalYAML accepts either shape. Both branches decode strictly, the way
// the top-level decoder does: yaml.Node.Decode drops the KnownFields setting,
// so each branch re-encodes the node and runs it through a strict decoder of
// its own, keeping a typo inside a model entry a startup error.
func (m *ModelsSection) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var entries []ModelEntry
		if err := strictDecodeNode(node, &entries); err != nil {
			return err
		}
		*m = ModelsSection{LLM: entries}
		return nil
	case yaml.MappingNode:
		type plain ModelsSection
		var section plain
		if err := strictDecodeNode(node, &section); err != nil {
			return err
		}
		*m = ModelsSection(section)
		return nil
	default:
		return fmt.Errorf("models: want a mapping with llm:/systemone: keys, or a plain list of llm models")
	}
}

// strictDecodeNode decodes one node with unknown-key rejection, which
// node.Decode alone does not do.
func strictDecodeNode(node *yaml.Node, out any) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// SystemOneSection is tenzing.yaml's systemone: block — the tuning for the
// decision model named by systemone_model:. Every field is optional and
// every sub-setting is a pointer, so "omitted" is distinguishable from "set
// to zero" and the feature's own defaults apply to whatever is left out.
//
// Defaults when the block is absent: the tool gate and the advisor-need
// judgment run; routing runs only when two or more models.llm: entries carry
// a description:, since fewer than two is not a choice.
type SystemOneSection struct {
	// RecentMessages is how many trailing conversation messages ride along
	// in every question's state. This content leaves the machine and
	// accuracy falls as the state fills with detail the decision does not
	// need, so it is small by default and 0 sends none.
	RecentMessages *int `yaml:"recent_messages"`

	Gate    SystemOneGate    `yaml:"gate"`
	Advisor SystemOneAdvisor `yaml:"advisor"`
	Routing SystemOneRouting `yaml:"routing"`
}

// SystemOneGate tunes tool gating. The thresholds are probabilities the
// model's answers are read against; each is validated here for range only,
// because the defaults they combine with live with the question wording they
// were calibrated for (internal/features/systemone). Setting ask_above above
// the effective deny_above is legal and simply means calls are denied rather
// than questioned.
type SystemOneGate struct {
	Enabled *bool `yaml:"enabled"`
	// AskAbove escalates a call to an approval prompt; DenyAbove refuses it
	// outright.
	AskAbove  *float64 `yaml:"ask_above"`
	DenyAbove *float64 `yaml:"deny_above"`
}

// SystemOneAdvisor tunes the advisor-need judgment. Turn it off where the
// advisor tool is not mounted — there would be nothing to consult.
type SystemOneAdvisor struct {
	Enabled      *bool    `yaml:"enabled"`
	ConsultAbove *float64 `yaml:"consult_above"`
}

// SystemOneRouting tunes model routing: which models.llm: entry serves the
// turn. Candidates are the entries carrying a description:, which becomes
// what the model is told the option is for. MinConfidence is the floor below
// which a choice is ignored and the configured model keeps serving.
type SystemOneRouting struct {
	Enabled       *bool    `yaml:"enabled"`
	MinConfidence *float64 `yaml:"min_confidence"`
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

// ConnectSection is tenzing.yaml's connect: block — the control-plane
// dial-out contract (see docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md).
// URL is the plane's ws:// or wss:// endpoint; setting it in the file puts
// the process in connect mode (like the --connect flag). Token is the bearer
// token on the upgrade request. Backoff is the reconnect delay base
// (doubles to a 30s cap); omit for the 1s default. EphemeralGrants keeps
// runtime-approved bash globs ("allow always") in memory for the process
// lifetime — the fleet default, true when omitted; false persists them to
// settings.json like serve mode.
type ConnectSection struct {
	URL             string    `yaml:"url"`
	Token           string    `yaml:"token"`
	Backoff         *Duration `yaml:"backoff"`
	EphemeralGrants *bool     `yaml:"ephemeral_grants"`
}

// ProviderTypes are the wire protocols a provider can speak — the three
// the repo actually implements, not a list of vendors. Most hosted APIs
// speak the OpenAI protocol, so DefaultProviderType covers them all and a
// provider entry usually needs no type at all; what distinguishes one such
// backend from another is its URL, not its name.
var ProviderTypes = []string{"anthropic", "ollama", DefaultProviderType, SystemOneProviderType}

// SystemOneProviderType is the System One evaluation protocol
// (POST <url>/v1/systemone), served by TypeSafe and by OpenRouter. It is
// named after the protocol rather than either vendor, like the other three.
// Models behind it are decision models, declared under models.systemone: and
// built as common.SystemOne — never as common.LLM.
const SystemOneProviderType = "systemone"

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
	// Description says what this model is for, in a sentence or two. It is
	// documentation with one consumer: a model carrying one becomes a
	// candidate for System One routing (systemone_model:), and the text is
	// what the decision model is told the option means. Omit it to keep a
	// model out of routing.
	Description string `yaml:"description"`
	// Vision marks the model as accepting image input; image-bearing
	// queries are rejected on models without it.
	Vision bool `yaml:"vision"`
	// ReasoningEffort sets the provider's reasoning tier for this model,
	// sent verbatim ("low"/"medium"/"high", plus "max" on Ollama). The
	// provider validates it; a bad value fails the first request, not
	// startup. Ignored by providers with no tier concept (Anthropic).
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// SystemOneEntry defines a System One (decision) model. The fields are the
// three that mean anything for one: Name is the local alias refs use,
// ModelName is the id sent on the wire ("jev-latest" at TypeSafe,
// "typesafe/jev-1.13" via OpenRouter), and Provider names an entry in
// File.Providers whose type is SystemOneProviderType.
//
// There is deliberately no context_window, max_response_tokens, vision or
// reasoning_effort: the answer is a fixed-size typed value that is not
// billed, the model takes no images and has no reasoning tier, and the token
// budgets are constants in the protocol package (systemone.MaxRequestTokens,
// systemone.MaxStateQuestionTokens) because nothing about them travels on
// the wire.
type SystemOneEntry struct {
	Provider  string `yaml:"provider"`
	Name      string `yaml:"name"`
	ModelName string `yaml:"model_name"`
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
		// client knows its vendor's endpoint. A systemone provider carries
		// TypeSafe's endpoint by default but may point elsewhere.
		case p.Type == DefaultProviderType && p.URL == "":
			return fmt.Errorf("providers[%d] (%s): url is required for %s providers", i, p.Name, DefaultProviderType)
		// The client appends /v1/systemone itself, so the url stops before
		// /v1. Copying an openai_compat base (".../api/v1") is the easy
		// mistake, and it produces /v1/v1/systemone and a 404 at runtime.
		case p.Type == SystemOneProviderType && strings.HasSuffix(strings.TrimSuffix(p.URL, "/"), "/v1"):
			return fmt.Errorf("providers[%d] (%s): url must stop before /v1 for %s providers — the client appends /v1/systemone (use %q)",
				i, p.Name, SystemOneProviderType, strings.TrimSuffix(strings.TrimSuffix(p.URL, "/"), "/v1"))
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
	providerTypes := map[string]string{}
	for _, p := range f.Providers {
		providers[p.Name] = true
		providerTypes[p.Name] = p.Type
	}

	// One alias namespace across both kinds: refs (model:, --model,
	// advisor_model:) name a bare alias, so the same name in both lists
	// would be ambiguous.
	models := map[string]bool{}
	for i, e := range f.Models.LLM {
		switch {
		case e.Name == "":
			return fmt.Errorf("models.llm[%d]: name is required", i)
		case e.ModelName == "":
			return fmt.Errorf("models.llm[%d] (%s): model_name is required", i, e.Name)
		case e.Provider == "":
			return fmt.Errorf("models.llm[%d] (%s): provider is required", i, e.Name)
		case !providers[e.Provider]:
			return fmt.Errorf("models.llm[%d] (%s): provider %q is not declared in providers:", i, e.Name, e.Provider)
		case providerTypes[e.Provider] == SystemOneProviderType:
			return fmt.Errorf("models.llm[%d] (%s): provider %q is a %s provider; declare this model under models.systemone:",
				i, e.Name, e.Provider, SystemOneProviderType)
		case models[e.Name]:
			return fmt.Errorf("models.llm[%d]: duplicate model name %q", i, e.Name)
		}
		models[e.Name] = true
	}

	for i, e := range f.Models.SystemOne {
		switch {
		case e.Name == "":
			return fmt.Errorf("models.systemone[%d]: name is required", i)
		case e.ModelName == "":
			return fmt.Errorf("models.systemone[%d] (%s): model_name is required", i, e.Name)
		case e.Provider == "":
			return fmt.Errorf("models.systemone[%d] (%s): provider is required", i, e.Name)
		case !providers[e.Provider]:
			return fmt.Errorf("models.systemone[%d] (%s): provider %q is not declared in providers:", i, e.Name, e.Provider)
		case providerTypes[e.Provider] != SystemOneProviderType:
			return fmt.Errorf("models.systemone[%d] (%s): provider %q has type %q; a System One model needs a %s provider",
				i, e.Name, e.Provider, providerTypes[e.Provider], SystemOneProviderType)
		case models[e.Name]:
			return fmt.Errorf("models.systemone[%d]: duplicate model name %q", i, e.Name)
		}
		models[e.Name] = true
	}

	if err := f.validateSystemOne(systemOneAliases(f.Models.SystemOne), models); err != nil {
		return err
	}

	if f.AdvisorNudge < 0 {
		return fmt.Errorf("advisor_nudge must be >= 0, got %d", f.AdvisorNudge)
	}
	if f.AdvisorCadence < -1 {
		return fmt.Errorf("advisor_cadence must be >= -1 (-1 = off), got %d", f.AdvisorCadence)
	}
	if f.AdvisorMaxCalls < 0 {
		return fmt.Errorf("advisor_max_calls must be >= 0, got %d", f.AdvisorMaxCalls)
	}
	return nil
}

// systemOneAliases collects the decision-model aliases a systemone_model:
// ref may name.
func systemOneAliases(entries []SystemOneEntry) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Name] = true
	}
	return out
}

// validateSystemOne checks the systemone_model: ref and the systemone:
// block. declared is every alias in the file, so naming a chat model can be
// reported as the wrong kind rather than as a missing one.
func (f File) validateSystemOne(systemOnes, declared map[string]bool) error {
	if f.SystemOne != nil && f.SystemOneModel == "" {
		return fmt.Errorf("systemone: has no effect without systemone_model:; name a models.systemone: entry or drop the block")
	}
	if f.SystemOneModel == "" {
		return nil
	}
	switch {
	case systemOnes[f.SystemOneModel]:
	case declared[f.SystemOneModel]:
		return fmt.Errorf("systemone_model %q is a chat model; it must name a models.systemone: entry", f.SystemOneModel)
	default:
		return fmt.Errorf("systemone_model %q is not declared under models.systemone:", f.SystemOneModel)
	}
	if f.SystemOne == nil {
		return nil
	}

	if n := f.SystemOne.RecentMessages; n != nil && *n < 0 {
		return fmt.Errorf("systemone.recent_messages must be >= 0, got %d", *n)
	}
	thresholds := []struct {
		key   string
		value *float64
	}{
		{"systemone.gate.ask_above", f.SystemOne.Gate.AskAbove},
		{"systemone.gate.deny_above", f.SystemOne.Gate.DenyAbove},
		{"systemone.advisor.consult_above", f.SystemOne.Advisor.ConsultAbove},
		{"systemone.routing.min_confidence", f.SystemOne.Routing.MinConfidence},
	}
	for _, t := range thresholds {
		if t.value != nil && (*t.value < 0 || *t.value > 1) {
			return fmt.Errorf("%s must be between 0 and 1, got %v", t.key, *t.value)
		}
	}

	// Routing asked for explicitly must have something to choose between.
	// Left unset it enables itself when the descriptions are there, so only
	// the explicit case can be wrong.
	if r := f.SystemOne.Routing.Enabled; r != nil && *r && f.describedModels() < 2 {
		return fmt.Errorf("systemone.routing.enabled needs at least two models.llm: entries with a description:, found %d",
			f.describedModels())
	}
	return nil
}

// describedModels counts the chat models eligible for routing.
func (f File) describedModels() int {
	n := 0
	for _, e := range f.Models.LLM {
		if strings.TrimSpace(e.Description) != "" {
			n++
		}
	}
	return n
}

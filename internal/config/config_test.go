package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenzing.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_FullFile(t *testing.T) {
	path := writeFile(t, `
model: main
subagent_model: sub
advisor_model: adv
advisor_nudge: 3
advisor_cadence: 4
advisor_max_calls: 12
max_turn_tokens: 100000
max_iterations: 50
max_wall_clock: "10m"
thinking_budget: 8192
subagent_depth: 0
approval_timeout: "90s"
no_permissions: true
dangerously_skip_permissions: true
read_only: true
thinking: false
no_session: true
no_context_files: true
system_file: sys.md
port: 9090
nexus_config: nx.yaml
debug: true
mcp_servers:
  - name: fs
    command: npx
    args: ["-y", "server-filesystem"]
providers:
  - name: router
    url: https://openrouter.ai/api/v1
    api_key: sk-test
    extra:
      provider.sort: throughput
models:
  - provider: router
    name: custom
    model_name: vendor/custom-4
    context_window: 200000
    max_response_tokens: 16384
    vision: true
    reasoning_effort: high
    cost: {input: 1.5, output: 6}
`)
	f, found, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("found = false")
	}

	if f.Model != "main" || f.SubagentModel != "sub" || f.AdvisorModel != "adv" {
		t.Errorf("model refs wrong: %+v", f)
	}
	if f.AdvisorCadence != 4 || f.AdvisorMaxCalls != 12 {
		t.Errorf("advisor_cadence=%d advisor_max_calls=%d, want 4 12", f.AdvisorCadence, f.AdvisorMaxCalls)
	}
	if f.AdvisorNudge != 3 || f.MaxTurnTokens != 100000 || f.MaxIterations != 50 {
		t.Errorf("numeric fields wrong: %+v", f)
	}
	if f.MaxWallClock.Value() != 10*time.Minute {
		t.Errorf("max_wall_clock = %v, want 10m", f.MaxWallClock.Value())
	}
	if f.ThinkingBudget == nil || *f.ThinkingBudget != 8192 {
		t.Errorf("thinking_budget: want 8192, got %v", f.ThinkingBudget)
	}
	if f.SubagentDepth == nil || *f.SubagentDepth != 0 {
		t.Errorf("subagent_depth: want explicit 0, got %v", f.SubagentDepth)
	}
	if f.ApprovalTimeout.Value() != 90*time.Second {
		t.Errorf("approval_timeout = %v, want 90s", f.ApprovalTimeout.Value())
	}
	if f.Thinking == nil || *f.Thinking != false {
		t.Errorf("thinking: want explicit false, got %v", f.Thinking)
	}
	if !f.NoPermissions || !f.SkipPermissions || !f.ReadOnly || !f.NoSession || !f.NoContextFiles || !f.Debug {
		t.Errorf("bool toggles wrong: %+v", f)
	}
	if f.Port == nil || *f.Port != 9090 || f.NexusConfig != "nx.yaml" {
		t.Errorf("serve fields wrong: %+v", f)
	}
	if f.SystemFile != "sys.md" {
		t.Errorf("path fields wrong: %+v", f)
	}
	if len(f.MCPServers) != 1 || f.MCPServers[0].Name != "fs" || f.MCPServers[0].Command != "npx" || len(f.MCPServers[0].Args) != 2 {
		t.Errorf("mcp_servers wrong: %+v", f.MCPServers)
	}
	if len(f.Providers) != 1 {
		t.Fatalf("providers wrong: %+v", f.Providers)
	}
	p := f.Providers[0]
	// type omitted in the fixture: it must come back defaulted.
	if p.Name != "router" || p.Type != DefaultProviderType || p.URL != "https://openrouter.ai/api/v1" || p.APIKey != "sk-test" {
		t.Errorf("provider wrong: %+v", p)
	}
	if got := p.Extra["provider.sort"]; got != "throughput" {
		t.Errorf("extra = %+v, want provider.sort: throughput", p.Extra)
	}
	if len(f.Models.LLM) != 1 || len(f.Models.SystemOne) != 0 {
		t.Fatalf("models wrong: %+v", f.Models)
	}
	e := f.Models.LLM[0]
	if e.Provider != "router" || e.Name != "custom" || e.ModelName != "vendor/custom-4" ||
		e.ContextWindow != 200000 || e.MaxResponseTokens != 16384 || !e.Vision || e.ReasoningEffort != "high" {
		t.Errorf("model entry wrong: %+v", e)
	}
	if e.Cost == nil || e.Cost.Input != 1.5 || e.Cost.Output != 6 {
		t.Errorf("cost wrong: %+v", e.Cost)
	}
}

func TestLoad_OmittedPointersStayNil(t *testing.T) {
	f, found, err := Load(writeFile(t, "model: x\n"), true)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if f.SubagentDepth != nil || f.ApprovalTimeout != nil || f.Thinking != nil || f.Port != nil || f.MaxWallClock != nil || f.ThinkingBudget != nil {
		t.Errorf("omitted pointer fields not nil: %+v", f)
	}
}

// provYAML is a minimal valid providers: section for cases that only care
// about what follows it. It ends mid-list, so a case can append another
// entry to the same providers: key rather than opening a second one (which
// yaml rejects before validation ever runs).
const provYAML = "providers:\n  - name: p\n    type: ollama\n    url: http://x\n"

// soProvYAML declares a System One provider, the only kind a models.systemone:
// entry may name.
const soProvYAML = "providers:\n  - name: so\n    type: systemone\n    url: https://api.typesafe.ai\n"

// soJevYAML declares both kinds of provider and one model of each, so a case
// can add systemone_model: to a file where "jev" resolves and "chat" is the
// wrong kind.
const soJevYAML = "providers:\n" +
	"  - name: so\n    type: systemone\n    url: https://api.typesafe.ai\n" +
	"  - name: p\n    type: ollama\n    url: http://x\n" +
	"models:\n" +
	"  llm:\n    - name: chat\n      provider: p\n      model_name: m\n" +
	"  systemone:\n    - name: jev\n      provider: so\n      model_name: jev-latest\n"

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"unknown key", "advisor_modle: x\n", "advisor_modle"},
		{"bad duration", "max_wall_clock: \"fast\"\n", "invalid duration"},
		{"bad duration type", "approval_timeout: [1]\n", "duration"},
		{"mcp server missing command", "mcp_servers:\n  - name: fs\n", "name and command are required"},
		{"negative nudge", "advisor_nudge: -1\n", "advisor_nudge"},
		{"cadence below -1", "advisor_cadence: -2\n", "advisor_cadence"},
		{"negative max calls", "advisor_max_calls: -1\n", "advisor_max_calls"},

		// Old schema: each removed spelling points at where it went.
		{"top-level base_url", "base_url: http://box:11434\n", "field base_url not found"},
		{"top-level api_key", "api_key: sk-x\n", "field api_key not found"},
		{"entry base_url", provYAML + "models:\n  - name: n\n    provider: p\n    model_name: m\n    base_url: http://x\n", "base_url"},

		// Providers.
		{"provider missing name", "providers:\n  - type: ollama\n    url: http://x\n", "name is required"},
		{"provider unknown type", "providers:\n  - name: p\n    type: llamafile\n    url: http://x\n", "unknown type"},
		{"provider missing url when compat", "providers:\n  - name: p\n    url: \"\"\n", "url is required for openai_compat"},

		{"vendor name is not a type", "providers:\n  - name: p\n    type: openrouter\n    url: http://x\n", "unknown type"},
		{"duplicate provider", provYAML + "  - name: p\n    type: ollama\n    url: http://y\n", "duplicate provider"},

		// Models.
		{"model missing name", provYAML + "models:\n  - provider: p\n    model_name: m\n", "name is required"},
		{"model missing model_name", provYAML + "models:\n  - name: n\n    provider: p\n", "model_name is required"},
		{"model missing provider", provYAML + "models:\n  - name: n\n    model_name: m\n", "provider is required"},
		{"model undeclared provider", provYAML + "models:\n  - name: n\n    provider: nope\n    model_name: m\n", "not declared in providers"},
		{"duplicate model", provYAML + "models:\n  - name: n\n    provider: p\n    model_name: m\n  - name: n\n    provider: p\n    model_name: m2\n", "duplicate model"},

		// System One models and the models: split.
		{"models wrong kind", provYAML + "models: 3\n", "want a mapping"},
		{"unknown models key", provYAML + "models:\n  llms:\n    - name: n\n", "llms"},
		{"nested entry unknown key", provYAML + "models:\n  llm:\n    - name: n\n      provider: p\n      model_name: m\n      base_url: http://x\n", "base_url"},
		{"systemone entry unknown key", soProvYAML + "models:\n  systemone:\n    - name: jev\n      provider: so\n      model_name: jev-latest\n      vision: true\n", "vision"},
		{"systemone missing model_name", soProvYAML + "models:\n  systemone:\n    - name: jev\n      provider: so\n", "models.systemone[0] (jev): model_name is required"},
		{"systemone undeclared provider", soProvYAML + "models:\n  systemone:\n    - name: jev\n      provider: nope\n      model_name: jev-latest\n", "not declared in providers"},
		{"systemone on chat provider", provYAML + "models:\n  systemone:\n    - name: jev\n      provider: p\n      model_name: jev-latest\n", "a System One model needs a systemone provider"},
		{"llm on systemone provider", soProvYAML + "models:\n  llm:\n    - name: n\n      provider: so\n      model_name: m\n", "declare this model under models.systemone:"},
		{"alias collides across kinds", soProvYAML + provYAML[len("providers:\n"):] + "models:\n  llm:\n    - name: dup\n      provider: p\n      model_name: m\n  systemone:\n    - name: dup\n      provider: so\n      model_name: jev-latest\n", "duplicate model name"},
		{"systemone url keeps v1", "providers:\n  - name: so\n    type: systemone\n    url: https://openrouter.ai/api/v1\n", "url must stop before /v1"},

		// systemone_model: and its block.
		{"systemone block without the ref", soJevYAML + "systemone:\n  recent_messages: 2\n", "has no effect without systemone_model"},
		{"systemone_model undeclared", soJevYAML + "systemone_model: nope\n", "not declared under models.systemone"},
		{"systemone_model names a chat model", soJevYAML + "systemone_model: chat\n", "is a chat model"},
		{"negative recent_messages", soJevYAML + "systemone_model: jev\nsystemone:\n  recent_messages: -1\n", "recent_messages must be >= 0"},
		{"irreversible_ask out of range", soJevYAML + "systemone_model: jev\nsystemone:\n  gate:\n    irreversible_ask: 1.5\n", "gate.irreversible_ask must be between 0 and 1"},
		{"secrets_ask out of range", soJevYAML + "systemone_model: jev\nsystemone:\n  gate:\n    secrets_ask: -0.1\n", "gate.secrets_ask must be between 0 and 1"},
		{"consult_above out of range", soJevYAML + "systemone_model: jev\nsystemone:\n  advisor:\n    consult_above: 2\n", "advisor.consult_above must be between 0 and 1"},
		{"min_confidence out of range", soJevYAML + "systemone_model: jev\nsystemone:\n  routing:\n    min_confidence: 9\n", "routing.min_confidence must be between 0 and 1"},
		{"routing on with nothing to choose", soJevYAML + "systemone_model: jev\nsystemone:\n  routing:\n    enabled: true\n", "at least two models.llm: entries with a description"},
		{"systemone block unknown key", soJevYAML + "systemone_model: jev\nsystemone:\n  gate:\n    ask_abov: 0.5\n", "ask_abov"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(writeFile(t, tt.content), true)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")

	if _, _, err := Load(missing, true); err == nil {
		t.Error("explicit missing file: want error, got nil")
	}

	f, found, err := Load(missing, false)
	if err != nil {
		t.Errorf("default-path missing file: err = %v, want nil", err)
	}
	if found {
		t.Error("default-path missing file: found = true, want false")
	}
	if f.Model != "" {
		t.Errorf("zero File expected, got %+v", f)
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	f, found, err := Load(writeFile(t, ""), true)
	if err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if !found {
		t.Error("empty file: found = false, want true")
	}
	if f.Model != "" || f.SubagentDepth != nil {
		t.Errorf("empty file should decode to zero File, got %+v", f)
	}
}

func TestLoad_ExpandsEnv(t *testing.T) {
	t.Setenv("TENZING_TEST_KEY", "sk-secret")

	tests := []struct {
		name string
		key  string
		want string
	}{
		{"bare", "$TENZING_TEST_KEY", "sk-secret"},
		{"braced", `"${TENZING_TEST_KEY}"`, "sk-secret"},
		{"unset left alone", "$TENZING_TEST_MISSING", "$TENZING_TEST_MISSING"},
		{"no reference", "plain", "plain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := "providers:\n  - name: p\n    type: ollama\n    url: http://x\n    api_key: " + tt.key + "\n"
			f, _, err := Load(writeFile(t, yaml), true)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := f.Providers[0].APIKey; got != tt.want {
				t.Errorf("api_key = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProviderTypeDefaulting covers the three ways type and url interact:
// an omitted type becomes openai_compat, which then needs a url; anthropic
// and ollama may omit the url because their clients know their endpoint.
func TestProviderTypeDefaulting(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantType string
		wantErr  string
	}{
		{
			name:     "omitted type defaults to openai_compat",
			yaml:     "providers:\n  - name: groq\n    url: https://api.groq.com/openai/v1\n",
			wantType: DefaultProviderType,
		},
		{
			name:     "explicit openai_compat",
			yaml:     "providers:\n  - name: groq\n    type: openai_compat\n    url: http://x\n",
			wantType: DefaultProviderType,
		},
		{
			name:     "anthropic may omit url",
			yaml:     "providers:\n  - name: claude\n    type: anthropic\n",
			wantType: "anthropic",
		},
		{
			name:     "ollama may omit url",
			yaml:     "providers:\n  - name: local\n    type: ollama\n",
			wantType: "ollama",
		},
		{
			name:    "compat without url is an error",
			yaml:    "providers:\n  - name: groq\n",
			wantErr: "url is required for openai_compat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, err := Load(writeFile(t, tt.yaml), true)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := f.Providers[0].Type; got != tt.wantType {
				t.Errorf("type = %q, want %q", got, tt.wantType)
			}
		})
	}
}

// TestTokenLimitKeysAreDistinct pins the rename that separated the two
// limits: the turn budget and a model's per-response cap used to share the
// name max_tokens, which made a config ambiguous about which one it set.
// Both old spellings must now be rejected outright.
func TestTokenLimitKeysAreDistinct(t *testing.T) {
	t.Run("both keys parse independently", func(t *testing.T) {
		f, _, err := Load(writeFile(t, provYAML+
			"max_turn_tokens: 100000\n"+
			"models:\n  - name: m\n    provider: p\n    model_name: w\n    max_response_tokens: 4096\n"), true)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if f.MaxTurnTokens != 100000 {
			t.Errorf("MaxTurnTokens = %d, want 100000", f.MaxTurnTokens)
		}
		if f.Models.LLM[0].MaxResponseTokens != 4096 {
			t.Errorf("MaxResponseTokens = %d, want 4096", f.Models.LLM[0].MaxResponseTokens)
		}
	})

	for _, tt := range []struct{ name, yaml string }{
		{"old top-level spelling", "max_tokens: 100000\n"},
		{"old model-entry spelling", provYAML +
			"models:\n  - name: m\n    provider: p\n    model_name: w\n    max_tokens: 4096\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Load(writeFile(t, tt.yaml), true); err == nil {
				t.Fatal("max_tokens should no longer be a valid key")
			}
		})
	}
}

// The models: block accepts the nested mapping and the pre-split sequence,
// and the two produce the same LLM list.
func TestLoad_ModelsSectionShapes(t *testing.T) {
	const legacy = provYAML +
		"models:\n" +
		"  - name: main\n    provider: p\n    model_name: w\n    context_window: 4096\n"
	const nested = provYAML +
		"  - name: so\n    type: systemone\n    url: https://api.typesafe.ai\n" +
		"models:\n" +
		"  llm:\n    - name: main\n      provider: p\n      model_name: w\n      context_window: 4096\n" +
		"  systemone:\n    - name: jev\n      provider: so\n      model_name: typesafe/jev-1.13\n"

	legacyFile, _, err := Load(writeFile(t, legacy), true)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	nestedFile, _, err := Load(writeFile(t, nested), true)
	if err != nil {
		t.Fatalf("Load nested: %v", err)
	}

	if len(legacyFile.Models.LLM) != 1 || len(legacyFile.Models.SystemOne) != 0 {
		t.Fatalf("legacy models = %+v, want one llm entry", legacyFile.Models)
	}
	if !reflect.DeepEqual(legacyFile.Models.LLM, nestedFile.Models.LLM) {
		t.Errorf("llm entries differ:\n legacy %+v\n nested %+v", legacyFile.Models.LLM, nestedFile.Models.LLM)
	}
	if len(nestedFile.Models.SystemOne) != 1 {
		t.Fatalf("systemone = %+v, want one entry", nestedFile.Models.SystemOne)
	}
	if e := nestedFile.Models.SystemOne[0]; e.Name != "jev" || e.Provider != "so" || e.ModelName != "typesafe/jev-1.13" {
		t.Errorf("systemone entry = %+v", e)
	}
}

// TestLoad_SystemOneSection pins the whole block round-tripping, including
// that a threshold set to zero survives as zero rather than reading as
// "omitted" — the reason every sub-setting is a pointer.
func TestLoad_SystemOneSection(t *testing.T) {
	path := writeFile(t, `
systemone_model: jev
systemone:
  recent_messages: 0
  gate:
    enabled: true
    irreversible_ask: 0.35
    secrets_ask: 0.55
  advisor:
    enabled: false
    consult_above: 0.8
  routing:
    enabled: true
    min_confidence: 0.4
providers:
  - name: so
    type: systemone
    url: https://api.typesafe.ai
  - name: p
    type: ollama
    url: http://x
models:
  llm:
    - name: main
      provider: p
      model_name: m
      description: Frontier. Refactors and design decisions.
    - name: fast
      provider: p
      model_name: m2
      description: Cheap and quick. Lookups and one-line edits.
  systemone:
    - name: jev
      provider: so
      model_name: jev-latest
`)
	file, _, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if file.SystemOneModel != "jev" {
		t.Fatalf("systemone_model = %q", file.SystemOneModel)
	}
	so := file.SystemOne
	if so == nil {
		t.Fatal("systemone block missing")
	}
	if so.RecentMessages == nil || *so.RecentMessages != 0 {
		t.Fatalf("recent_messages = %v, want a set 0", so.RecentMessages)
	}
	if so.Gate.Enabled == nil || !*so.Gate.Enabled {
		t.Fatalf("gate.enabled = %v", so.Gate.Enabled)
	}
	if so.Advisor.Enabled == nil || *so.Advisor.Enabled {
		t.Fatalf("advisor.enabled = %v, want a set false", so.Advisor.Enabled)
	}
	if so.Gate.IrreversibleAsk == nil || *so.Gate.IrreversibleAsk != 0.35 || so.Gate.SecretsAsk == nil || *so.Gate.SecretsAsk != 0.55 {
		t.Fatalf("gate thresholds = %v/%v", so.Gate.IrreversibleAsk, so.Gate.SecretsAsk)
	}
	if so.Advisor.ConsultAbove == nil || *so.Advisor.ConsultAbove != 0.8 {
		t.Fatalf("consult_above = %v", so.Advisor.ConsultAbove)
	}
	if so.Routing.MinConfidence == nil || *so.Routing.MinConfidence != 0.4 {
		t.Fatalf("min_confidence = %v", so.Routing.MinConfidence)
	}
	if file.Models.LLM[0].Description == "" || file.Models.LLM[1].Description == "" {
		t.Fatalf("descriptions lost: %+v", file.Models.LLM)
	}
}

// TestLoad_SystemOneModelWithoutBlock: the scalar alone is the common case —
// it must load, leaving every knob to the feature's own defaults.
func TestLoad_SystemOneModelWithoutBlock(t *testing.T) {
	file, _, err := Load(writeFile(t, soJevYAML+"systemone_model: jev\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if file.SystemOne != nil {
		t.Fatalf("absent block must stay nil, got %+v", file.SystemOne)
	}
	if file.SystemOneModel != "jev" {
		t.Fatalf("systemone_model = %q", file.SystemOneModel)
	}
}

// TestLoad_RoutingUnsetNeedsNoDescriptions: only routing asked for
// explicitly is checked against the candidate count; left unset it enables
// itself when the descriptions exist.
func TestLoad_RoutingUnsetNeedsNoDescriptions(t *testing.T) {
	content := soJevYAML + "systemone_model: jev\nsystemone:\n  routing:\n    min_confidence: 0.6\n"
	if _, _, err := Load(writeFile(t, content), true); err != nil {
		t.Fatalf("unset routing must not require candidates: %v", err)
	}
}

func TestDescribedModelsCountsOnlyNonBlank(t *testing.T) {
	f := File{Models: ModelsSection{LLM: []ModelEntry{
		{Name: "a", Description: "does things"},
		{Name: "b", Description: "   "},
		{Name: "c"},
	}}}
	if got := f.describedModels(); got != 1 {
		t.Fatalf("describedModels = %d, want 1 (blank and absent do not count)", got)
	}
}

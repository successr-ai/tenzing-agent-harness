package modelregistry

import (
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"go.yaml.in/yaml/v3"
)

// MergeProviderFlags layers --provider JSON definitions over the config
// file's providers: list. A flag whose name matches a file entry replaces
// it in place; a new name is appended. Each flag value is one provider
// object, and every field it omits is omitted — the flag replaces an entry
// rather than patching it, so a partial override would silently drop the
// url or key it left out.
func MergeProviderFlags(file []config.Provider, flags []string) ([]config.Provider, error) {
	if len(flags) == 0 {
		return file, nil
	}
	out := append([]config.Provider(nil), file...)
	for i, raw := range flags {
		var p config.Provider
		if err := yaml.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("--provider[%d]: %w", i, err)
		}
		if p.Name == "" {
			return nil, fmt.Errorf("--provider[%d]: name is required", i)
		}
		if idx := providerIndex(out, p.Name); idx >= 0 {
			out[idx] = p
			continue
		}
		out = append(out, p)
	}
	if err := config.ValidateProviders(out); err != nil {
		return nil, err
	}
	return out, nil
}

func providerIndex(ps []config.Provider, name string) int {
	for i, p := range ps {
		if p.Name == name {
			return i
		}
	}
	return -1
}
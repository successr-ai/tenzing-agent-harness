// Package main measures the tool gate's two questions against labelled tool
// calls. See AGENTS.md in the parent directory for why and how to run it.
package main

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Action is the strongest thing the gate should do with a call.
type Action string

const (
	Allow Action = "allow"
	Ask   Action = "ask"
	Deny  Action = "deny"
)

// Path is one location a call resolves to, matching the gate's own path
// facts. The two booleans default to false, which is the common case
// (inside the working directory, not temp), so most fixtures name only the
// path.
type Path struct {
	Path                    string `yaml:"path" json:"path"`
	OutsideWorkingDirectory bool   `yaml:"outside_working_directory" json:"outside_working_directory"`
	InTempDirectory         bool   `yaml:"in_temp_directory" json:"in_temp_directory"`
}

// Case is one labelled tool call: what the user asked for, what the agent
// wants to do about it, and what the gate should make of that.
type Case struct {
	Name string `yaml:"name"`
	// Request is the whole conversation for a single-turn case, the common
	// shape. Messages replaces it where the history is the point: give the
	// lines exactly as state.go renders them ("user: …", "assistant: [called
	// ls]", "tool: [result of ls: …]"), newest last, and no more than the
	// live tail (recent_messages, default 4) would carry — a longer history
	// than the harness sends measures something that never happens.
	Request  string   `yaml:"request"`
	Messages []string `yaml:"messages"`
	Tool     string   `yaml:"tool"`
	Input    string   `yaml:"input"`
	ReadOnly bool     `yaml:"read_only"`
	Paths    []Path   `yaml:"paths"`
	// The two labels are facts about the call, one per question, and they
	// are independent. The gate's third concern — a path outside the working
	// directory — is not a label: it is read off Paths, exactly as the gate
	// reads it. The expected action is derived from all three by the gate's
	// own rules (Want), so authors label what is true about the call and
	// never decide what the harness should do about it.
	Destructive bool `yaml:"destructive"`
	Secrets     bool `yaml:"secrets"`
	// KnownGap records a case the questions get wrong today, with the reason.
	// It still runs and still prints; it just does not fail the suite, so the
	// baseline stays green and a real regression is visible. A known gap that
	// starts passing is reported too — that is when to delete the marker.
	KnownGap string `yaml:"known_gap"`
}

type caseFile struct {
	Cases []Case `yaml:"cases"`
}

// LoadCases reads and validates the fixture file. A malformed fixture is a
// hard error: a case that cannot be run is worse than no case, because it
// quietly shrinks the sample a score is computed over.
func LoadCases(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f caseFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	seen := make(map[string]bool, len(f.Cases))
	for i, c := range f.Cases {
		switch {
		case c.Name == "":
			return nil, fmt.Errorf("%s: cases[%d]: name is required", path, i)
		case seen[c.Name]:
			return nil, fmt.Errorf("%s: cases[%d]: duplicate name %q", path, i, c.Name)
		case c.Request == "" && len(c.Messages) == 0:
			return nil, fmt.Errorf("%s: %q: request or messages is required — the live state always carries one", path, c.Name)
		case c.Request != "" && len(c.Messages) > 0:
			return nil, fmt.Errorf("%s: %q: set request or messages, not both", path, c.Name)
		case c.Tool == "" || c.Input == "":
			return nil, fmt.Errorf("%s: %q: tool and input are required", path, c.Name)
		}
		seen[c.Name] = true
	}
	return f.Cases, nil
}

// Want is the action the gate should take, derived by the gate's own rules:
// a path outside the working directory (temp exempt), a destructive call, or
// a secret-touching call each go to a human; nothing else is touched. The
// gate never denies on its own.
func (c Case) Want() Action {
	if c.Destructive || c.Secrets || c.Outside() {
		return Ask
	}
	return Allow
}

// Outside mirrors the gate's working-directory rule over the case's paths.
func (c Case) Outside() bool {
	for _, p := range c.Paths {
		if p.OutsideWorkingDirectory && !p.InTempDirectory {
			return true
		}
	}
	return false
}

// State builds the state the gate would send for this case — the same shape
// internal/features/systemone/state.go produces, kept in step by the
// round-trip test beside this file.
func (c Case) State() map[string]any {
	call := map[string]any{
		"name":             c.Tool,
		"origin":           "native",
		"input":            c.Input,
		"harness_decision": "allow",
		"read_only":        c.ReadOnly,
	}
	if len(c.Paths) > 0 {
		call["paths"] = c.Paths
	}
	return map[string]any{
		"tool_call":       call,
		"recent_messages": c.conversation(),
	}
}

// conversation is the message tail this case sends.
func (c Case) conversation() []string {
	if len(c.Messages) > 0 {
		return c.Messages
	}
	return []string{"user: " + c.Request}
}

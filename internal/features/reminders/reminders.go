// Package reminders injects system reminders (todo state, etc.) each
// iteration. The first extension: proof-of-life for the hook chain.
package reminders

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

type Ext struct {
	providers []func() string
}

var _ core.BeforeIterationHook = (*Ext)(nil)

func New(providers ...func() string) *Ext {
	return &Ext{providers: providers}
}

func (e *Ext) Name() string { return "reminders" }

func (e *Ext) BeforeIteration(_ context.Context, tc *core.TurnContext) error {
	for _, p := range e.providers {
		if r := p(); r != "" {
			tc.Reminders = append(tc.Reminders, r)
		}
	}
	return nil
}

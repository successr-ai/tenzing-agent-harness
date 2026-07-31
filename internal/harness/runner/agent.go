package runner

import (
	"github.com/successr-ai/tenzing-agent-harness/internal/core"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// AgentBuilder creates a core.Agent given an LLM and system prompt.
type AgentBuilder func(llm common.LLM, systemPrompt string) (core.Agent, error)

package models

import (
	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Every model on https://platform.claude.com/docs/en/about-claude/models/overview
// (current + legacy). Context window and max output are the synchronous
// Messages API values from that page.
var (
	// Current models

	Anthropic_ClaudeFable5 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeFable5,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// The SDK has no typed constant for Claude Opus 5 yet; the bare ID is the
	// documented form.
	Anthropic_ClaudeOpus5 Model = common.ModelDefinition{
		Name:              "claude-opus-5",
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeSonnet5 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeSonnet5,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeHaiku4_5 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeHaiku4_5,
		MaxTokens:         64_000,
		ContextWindowSize: 200_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// Legacy models

	Anthropic_ClaudeOpus4_8 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeOpus4_8,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeOpus4_7 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeOpus4_7,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeOpus4_6 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeOpus4_6,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeSonnet4_6 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeSonnet4_6,
		MaxTokens:         128_000,
		ContextWindowSize: 1_000_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeSonnet4_5 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeSonnet4_5,
		MaxTokens:         64_000,
		ContextWindowSize: 200_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	Anthropic_ClaudeOpus4_5 Model = common.ModelDefinition{
		Name:              anthropicSDK.ModelClaudeOpus4_5,
		MaxTokens:         64_000,
		ContextWindowSize: 200_000,
		Provider:          "anthropic",
		SupportsThinking:  true,
		SupportsVision:    true,
	}
)

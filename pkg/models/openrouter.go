package models

import "github.com/successr-ai/tenzing-agent-harness/pkg/common"

// Selected models from https://openrouter.ai/models. Context window and
// MaxTokens come from OpenRouter's models API (context_length and
// top_provider.max_completion_tokens). Models whose top provider publishes no
// completion cap use a conservative 32K MaxTokens.
var (
	OpenRouter_DeepSeekV4Flash Model = common.ModelDefinition{
		Name:              "deepseek/deepseek-v4-flash",
		MaxTokens:         32_768,
		ContextWindowSize: 1_048_576,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_DeepSeekV4Pro Model = common.ModelDefinition{
		Name:              "deepseek/deepseek-v4-pro",
		MaxTokens:         384_000,
		ContextWindowSize: 1_048_576,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_GLM5_2 Model = common.ModelDefinition{
		Name:              "z-ai/glm-5.2",
		MaxTokens:         131_072,
		ContextWindowSize: 1_048_576,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_MinimaxM3 Model = common.ModelDefinition{
		Name:              "minimax/minimax-m3",
		MaxTokens:         512_000,
		ContextWindowSize: 1_048_576,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_KimiK3 Model = common.ModelDefinition{
		Name:              "moonshotai/kimi-k3",
		MaxTokens:         32_768,
		ContextWindowSize: 1_048_576,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_KimiK2_7Code Model = common.ModelDefinition{
		Name:              "moonshotai/kimi-k2.7-code",
		MaxTokens:         262_144,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_KimiK2_6 Model = common.ModelDefinition{
		Name:              "moonshotai/kimi-k2.6",
		MaxTokens:         262_144,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}

	OpenRouter_Gemma4_31B Model = common.ModelDefinition{
		Name:              "google/gemma-4-31b-it",
		MaxTokens:         262_144,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
	}

	OpenRouter_Gemma4_26B_A4B Model = common.ModelDefinition{
		Name:              "google/gemma-4-26b-a4b-it",
		MaxTokens:         262_144,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
	}

	OpenRouter_Gemma4_31B_Free Model = common.ModelDefinition{
		Name:              "google/gemma-4-31b-it:free",
		MaxTokens:         32_768,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
	}

	OpenRouter_Gemma4_26B_A4B_Free Model = common.ModelDefinition{
		Name:              "google/gemma-4-26b-a4b-it:free",
		MaxTokens:         32_768,
		ContextWindowSize: 262_144,
		Provider:          "openrouter",
	}

	OpenRouter_Grok4_5 Model = common.ModelDefinition{
		Name:              "x-ai/grok-4.5",
		MaxTokens:         32_768,
		ContextWindowSize: 500_000,
		Provider:          "openrouter",
		SupportsThinking:  true,
	}
)

package models

import "github.com/successr-ai/tenzing-agent-harness/pkg/common"

// Models hosted by Lightning AI itself (the "Lightning AI" section of
// https://lightning.ai/models?section=lightning). Third-party pass-through
// models (OpenAI, Anthropic, Google) are not defined here.
var (
	Lightning_Nemotron3Ultra Model = common.ModelDefinition{
		Name:              "lightning-ai/nvidia-nemotron-3-ultra-550b-a55b",
		MaxTokens:         32_768,
		ContextWindowSize: 262_144,
		Provider:          "lightning",
		SupportsThinking:  true,
	}

	Lightning_Nemotron3NanoOmni_30B Model = common.ModelDefinition{
		Name:              "lightning-ai/nvidia-nemotron-3-nano-omni-30b-a3b",
		MaxTokens:         32_768,
		ContextWindowSize: 262_144,
		Provider:          "lightning",
		SupportsThinking:  true,
	}

	Lightning_DeepSeekV4Pro Model = common.ModelDefinition{
		Name:              "lightning-ai/deepseek-v4-pro",
		MaxTokens:         32_768,
		ContextWindowSize: 1_048_576,
		Provider:          "lightning",
		SupportsThinking:  true,
	}

	// Gemma 4 publishes no output cap; 32K is a conservative ceiling.
	Lightning_Gemma4_31B Model = common.ModelDefinition{
		Name:              "lightning-ai/gemma-4-31B-it",
		MaxTokens:         32_768,
		ContextWindowSize: 131_072,
		Provider:          "lightning",
	}

	Lightning_GPTOSS_20B Model = common.ModelDefinition{
		Name:              "lightning-ai/gpt-oss-20b",
		MaxTokens:         32_768,
		ContextWindowSize: 131_072,
		Provider:          "lightning",
		SupportsThinking:  true,
	}

	Lightning_GPTOSS_120B Model = common.ModelDefinition{
		Name:              "lightning-ai/gpt-oss-120b",
		MaxTokens:         32_768,
		ContextWindowSize: 131_072,
		Provider:          "lightning",
		SupportsThinking:  true,
	}
)

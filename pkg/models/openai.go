package models

import (
	"github.com/openai/openai-go/v3"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Text-generation models from https://developers.openai.com/api/docs/models
// (full catalog at /api/docs/models/all). Context window and max output are
// each model page's published values. Image, video, audio, realtime,
// transcription, speech, embedding, moderation, computer-use, and legacy
// completions-only models are excluded — they aren't usable through this
// library's chat interface. Models without an openai-go v3 constant use the
// documented ID string directly.
var (
	// GPT-5.6

	OpenAI_GPT5_6Sol Model = common.ModelDefinition{
		Name:              "gpt-5.6-sol",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_6Terra Model = common.ModelDefinition{
		Name:              "gpt-5.6-terra",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_6Luna Model = common.ModelDefinition{
		Name:              "gpt-5.6-luna",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5.5

	OpenAI_GPT5_5 Model = common.ModelDefinition{
		Name:              "gpt-5.5",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_5Pro Model = common.ModelDefinition{
		Name:              "gpt-5.5-pro",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5.4

	OpenAI_GPT5_4 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_4,
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_4Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_4Mini,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_4Nano Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_4Nano,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_4Pro Model = common.ModelDefinition{
		Name:              "gpt-5.4-pro",
		MaxTokens:         128_000,
		ContextWindowSize: 1_050_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5.3

	OpenAI_GPT5_3ChatLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_3ChatLatest,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT5_3Codex Model = common.ModelDefinition{
		Name:              "gpt-5.3-codex",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5.2

	OpenAI_GPT5_2 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_2,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_2ChatLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_2ChatLatest,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT5_2Codex Model = common.ModelDefinition{
		Name:              "gpt-5.2-codex",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_2Pro Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_2Pro,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5.1

	OpenAI_GPT5_1 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_1,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_1ChatLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_1ChatLatest,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT5_1Codex Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5_1Codex,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_1CodexMax Model = common.ModelDefinition{
		Name:              "gpt-5.1-codex-max",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5_1CodexMini Model = common.ModelDefinition{
		Name:              "gpt-5.1-codex-mini",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// GPT-5

	OpenAI_GPT5 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5ChatLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5ChatLatest,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT5Codex Model = common.ModelDefinition{
		Name:              "gpt-5-codex",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5Mini,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5Nano Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT5Nano,
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_GPT5Pro Model = common.ModelDefinition{
		Name:              "gpt-5-pro",
		MaxTokens:         272_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	// OpenAI_ChatLatest tracks the model currently used by ChatGPT.
	OpenAI_ChatLatest Model = common.ModelDefinition{
		Name:              "chat-latest",
		MaxTokens:         128_000,
		ContextWindowSize: 400_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	// o-series reasoning

	OpenAI_O1 Model = common.ModelDefinition{
		Name:              openai.ChatModelO1,
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_O1Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelO1Mini,
		MaxTokens:         65_536,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	OpenAI_O1Preview Model = common.ModelDefinition{
		Name:              openai.ChatModelO1Preview,
		MaxTokens:         32_768,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	OpenAI_O1Pro Model = common.ModelDefinition{
		Name:              "o1-pro",
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_O3 Model = common.ModelDefinition{
		Name:              openai.ChatModelO3,
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_O3Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelO3Mini,
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	OpenAI_O3Pro Model = common.ModelDefinition{
		Name:              "o3-pro",
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_O3DeepResearch Model = common.ModelDefinition{
		Name:              "o3-deep-research",
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	OpenAI_O4Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelO4Mini,
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
		SupportsVision:    true,
	}

	OpenAI_O4MiniDeepResearch Model = common.ModelDefinition{
		Name:              "o4-mini-deep-research",
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	// Codex

	OpenAI_CodexMiniLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelCodexMiniLatest,
		MaxTokens:         100_000,
		ContextWindowSize: 200_000,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	// GPT-4.1 / GPT-4o / GPT-4 / GPT-3.5

	OpenAI_GPT4_1 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4_1,
		MaxTokens:         32_768,
		ContextWindowSize: 1_047_576,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4_1Mini Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4_1Mini,
		MaxTokens:         32_768,
		ContextWindowSize: 1_047_576,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4_1Nano Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4_1Nano,
		MaxTokens:         32_768,
		ContextWindowSize: 1_047_576,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4_5Preview Model = common.ModelDefinition{
		Name:              "gpt-4.5-preview",
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4o Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4o,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4oMini Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4oMini,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4oSearchPreview Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4oSearchPreview,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
	}

	OpenAI_GPT4oMiniSearchPreview Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4oMiniSearchPreview,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
	}

	OpenAI_ChatGPT4oLatest Model = common.ModelDefinition{
		Name:              openai.ChatModelChatgpt4oLatest,
		MaxTokens:         16_384,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4Turbo Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4Turbo,
		MaxTokens:         4_096,
		ContextWindowSize: 128_000,
		Provider:          "openai",
		SupportsVision:    true,
	}

	OpenAI_GPT4TurboPreview Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4TurboPreview,
		MaxTokens:         4_096,
		ContextWindowSize: 128_000,
		Provider:          "openai",
	}

	OpenAI_GPT4 Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT4,
		MaxTokens:         8_192,
		ContextWindowSize: 8_192,
		Provider:          "openai",
	}

	OpenAI_GPT3_5Turbo Model = common.ModelDefinition{
		Name:              openai.ChatModelGPT3_5Turbo,
		MaxTokens:         4_096,
		ContextWindowSize: 16_385,
		Provider:          "openai",
	}

	// Open-weight

	OpenAI_GPTOSS_120B Model = common.ModelDefinition{
		Name:              "gpt-oss-120b",
		MaxTokens:         131_072,
		ContextWindowSize: 131_072,
		Provider:          "openai",
		SupportsThinking:  true,
	}

	OpenAI_GPTOSS_20B Model = common.ModelDefinition{
		Name:              "gpt-oss-20b",
		MaxTokens:         131_072,
		ContextWindowSize: 131_072,
		Provider:          "openai",
		SupportsThinking:  true,
	}
)

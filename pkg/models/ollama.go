package models

import "github.com/successr-ai/tenzing-agent-harness/pkg/common"

// One definition per model on https://ollama.com/search, one entry per primary
// tag (parameter-size tags and cloud tags). Quantization/format variants
// (q4_K_M, q8_0, bf16, mlx, mxfp8, nvfp4, coding, mtp) share the primary tag's
// definition — pass the full tag as Name if needed. Tags ending in ":cloud"
// run on Ollama Cloud infrastructure, not locally, and require an Ollama API key.
var (
	// DeepSeek

	Ollama_DeepSeekV4Flash_Cloud Model = common.ModelDefinition{
		Name:                 "deepseek-v4-flash:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    1_048_576,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_DeepSeekV4Pro_Cloud Model = common.ModelDefinition{
		Name:                 "deepseek-v4-pro:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    1_048_576,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	// Google Gemma

	Ollama_Gemma4_E2B Model = common.ModelDefinition{
		Name:                 "gemma4:e2b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Gemma4_E4B Model = common.ModelDefinition{
		Name:                 "gemma4:e4b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Gemma4_12B Model = common.ModelDefinition{
		Name:                 "gemma4:12b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Gemma4_26B Model = common.ModelDefinition{
		Name:                 "gemma4:26b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Gemma4_31B Model = common.ModelDefinition{
		Name:                 "gemma4:31b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Gemma4_Cloud Model = common.ModelDefinition{
		Name:                 "gemma4:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// Z.ai GLM

	Ollama_GLM5_1_Cloud Model = common.ModelDefinition{
		Name:                 "glm-5.1:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    202_752,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	// Ollama_GLM5_2_Cloud is Z.ai's GLM-5.2 served via Ollama Cloud — inference
	// runs on Ollama/Z.ai infrastructure, not locally. Requires an Ollama API key.
	Ollama_GLM5_2_Cloud Model = common.ModelDefinition{
		Name:                 "glm-5.2:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    999_424,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_GLMOCR Model = common.ModelDefinition{
		Name:                 "glm-ocr",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// IBM Granite

	Ollama_Granite4_1_3B Model = common.ModelDefinition{
		Name:                 "granite4.1:3b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Granite4_1_8B Model = common.ModelDefinition{
		Name:                 "granite4.1:8b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Granite4_1_30B Model = common.ModelDefinition{
		Name:                 "granite4.1:30b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// Moonshot AI Kimi

	Ollama_KimiK2_6_Cloud Model = common.ModelDefinition{
		Name:                 "kimi-k2.6:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_KimiK2_7Code_Cloud Model = common.ModelDefinition{
		Name:                 "kimi-k2.7-code:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	// Laguna

	Ollama_LagunaS2_1 Model = common.ModelDefinition{
		Name:                 "laguna-s-2.1",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_LagunaXS2_1 Model = common.ModelDefinition{
		Name:                 "laguna-xs-2.1",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// Liquid LFM

	Ollama_LFM2_24B Model = common.ModelDefinition{
		Name:                 "lfm2:24b",
		MaxTokens:            32_768,
		ContextWindowSize:    32_768,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// MiniMax

	Ollama_MinimaxM2_5_Cloud Model = common.ModelDefinition{
		Name:                 "minimax-m2.5:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    202_752,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_MinimaxM2_7_Cloud Model = common.ModelDefinition{
		Name:                 "minimax-m2.7:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    204_800,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_MinimaxM3_Cloud Model = common.ModelDefinition{
		Name:                 "minimax-m3:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    524_288,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	// NVIDIA Nemotron

	Ollama_Nemotron3Super_120B Model = common.ModelDefinition{
		Name:                 "nemotron-3-super:120b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Nemotron3Super_Cloud Model = common.ModelDefinition{
		Name:                 "nemotron-3-super:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Nemotron3_33B Model = common.ModelDefinition{
		Name:                 "nemotron3:33b",
		MaxTokens:            32_768,
		ContextWindowSize:    131_072,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	// Ornith

	Ollama_Ornith_9B Model = common.ModelDefinition{
		Name:                 "ornith:9b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	Ollama_Ornith_35B Model = common.ModelDefinition{
		Name:                 "ornith:35b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
	}

	// Alibaba Qwen

	Ollama_Qwen3_5_0_8B Model = common.ModelDefinition{
		Name:                 "qwen3.5:0.8b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_2B Model = common.ModelDefinition{
		Name:                 "qwen3.5:2b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_4B Model = common.ModelDefinition{
		Name:                 "qwen3.5:4b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_9B Model = common.ModelDefinition{
		Name:                 "qwen3.5:9b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_27B Model = common.ModelDefinition{
		Name:                 "qwen3.5:27b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_35B Model = common.ModelDefinition{
		Name:                 "qwen3.5:35b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_122B Model = common.ModelDefinition{
		Name:                 "qwen3.5:122b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_5_Cloud Model = common.ModelDefinition{
		Name:                 "qwen3.5:cloud",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_6_27B Model = common.ModelDefinition{
		Name:                 "qwen3.6:27b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}

	Ollama_Qwen3_6_35B Model = common.ModelDefinition{
		Name:                 "qwen3.6:35b",
		MaxTokens:            32_768,
		ContextWindowSize:    262_144,
		DefaultContextWindow: 32_768,
		Provider:             "ollama",
		SupportsThinking:     true,
	}
)

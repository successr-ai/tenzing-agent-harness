package models

import "github.com/successr-ai/tenzing-agent-harness/pkg/common"

var (
	// Paid-tier limits; Cerebras free tier caps at 65K context / 32K output.
	Cerebras_GPTOSS_120B Model = common.ModelDefinition{
		Name:              "gpt-oss-120b",
		MaxTokens:         40_960,
		ContextWindowSize: 131_072,
		Provider:          "cerebras",
		SupportsThinking:  true,
	}
)

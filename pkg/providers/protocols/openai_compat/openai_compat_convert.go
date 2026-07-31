package openai_compat

// Converters between the provider-agnostic request/response types and the
// OpenAI SDK's wire types, shared by every OpenAI-compatible provider.

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func toOpenAIMessages(msgs []common.Message) []openai.ChatCompletionMessageParamUnion {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case common.RoleUser:
			result = append(result, toOpenAIUserMessage(msg))
		case common.RoleAssistant:
			result = append(result, toOpenAIAssistantMessage(msg))
		case common.RoleSystem:
			result = append(result, openai.SystemMessage(common.CombinedText(msg.Content)))
		case common.RoleTool:
			for _, block := range msg.Content {
				if block.Type == common.ContentTypeToolResult {
					result = append(result, openai.ToolMessage(block.ToolOutput, block.ToolResultID))
				}
			}
		}
	}
	return result
}

// toOpenAIUserMessage builds a user message: a plain string when the message
// is text-only, or a content-part array (text + image_url parts in block
// order) when it carries images. Image data rides as a base64 data: URI —
// the OpenAI-compatible wire format for inline images.
func toOpenAIUserMessage(msg common.Message) openai.ChatCompletionMessageParamUnion {
	hasImage := false
	for _, block := range msg.Content {
		if block.Type == common.ContentTypeImage && block.Image != nil {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return openai.UserMessage(common.CombinedText(msg.Content))
	}

	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case common.ContentTypeText:
			parts = append(parts, openai.TextContentPart(block.Text))
		case common.ContentTypeImage:
			if block.Image == nil {
				continue
			}
			parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: "data:" + block.Image.MediaType + ";base64," + block.Image.Data,
			}))
		}
	}
	return openai.UserMessage(parts)
}

func toOpenAIAssistantMessage(msg common.Message) openai.ChatCompletionMessageParamUnion {
	assistant := openai.ChatCompletionAssistantMessageParam{}

	if text := common.CombinedText(msg.Content); text != "" {
		assistant.Content.OfString = param.NewOpt(text)
	}

	for _, block := range msg.Content {
		if block.Type != common.ContentTypeToolUse {
			continue
		}
		assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: block.ToolUseID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      block.ToolName,
					Arguments: string(block.ToolInput),
				},
			},
		})
	}

	return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}

func toOpenAITools(tools []common.ToolDefinition) ([]openai.ChatCompletionToolUnionParam, error) {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var params shared.FunctionParameters
		if tool.InputSchema != nil {
			if err := json.Unmarshal(tool.InputSchema, &params); err != nil {
				return nil, fmt.Errorf("tool %q: parse input schema: %w", tool.Name, err)
			}
		}

		result = append(result, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        tool.Name,
			Description: param.NewOpt(tool.Description),
			Parameters:  params,
		}))
	}
	return result, nil
}

func fromOpenAIResponse(res *openai.ChatCompletion) common.CompletionResponse {
	var content []common.ContentBlock
	var stopReason common.StopReason

	if len(res.Choices) > 0 {
		choice := res.Choices[0]
		stopReason = fromOpenAIFinishReason(choice.FinishReason)

		if choice.Message.Content != "" {
			content = append(content, common.NewTextContent(choice.Message.Content))
		}

		for _, tc := range choice.Message.ToolCalls {
			if tc.Type == "function" {
				content = append(content, common.NewToolUseContent(
					tc.ID,
					tc.Function.Name,
					json.RawMessage(tc.Function.Arguments),
				))
			}
		}
	}

	return common.CompletionResponse{
		ID:         res.ID,
		Content:    content,
		StopReason: stopReason,
		Usage: common.Usage{
			InputTokens:  res.Usage.PromptTokens,
			OutputTokens: res.Usage.CompletionTokens,
		},
		Model: res.Model,
	}
}

// reasoningEffortForBudget maps a token budget onto OpenAI's coarse
// reasoning_effort tiers. Lossy by design: the API has no numeric budget.
func reasoningEffortForBudget(budget int64) openai.ReasoningEffort {
	switch {
	case budget < 4096:
		return openai.ReasoningEffortLow
	case budget < 16384:
		return openai.ReasoningEffortMedium
	default:
		return openai.ReasoningEffortHigh
	}
}

func fromOpenAIFinishReason(reason string) common.StopReason {
	switch reason {
	case "stop":
		return common.StopReasonStop
	case "length":
		return common.StopReasonMaxTokens
	case "tool_calls":
		return common.StopReasonToolUse
	default:
		return common.StopReason(reason)
	}
}

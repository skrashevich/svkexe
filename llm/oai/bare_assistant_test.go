package oai

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

// TestBareAssistantMessage tests that assistant messages don't serialize to bare {"role": "assistant"}
// This was causing 400 Bad Request errors with llama.cpp API provider (Issue #223)
func TestBareAssistantMessage(t *testing.T) {
	tests := []struct {
		name            string
		msg             llm.Message
		wantContentFull bool // true if we want content field to be non-empty
	}{
		{
			name: "empty text content",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: ""},
				},
			},
			wantContentFull: true, // Should have " " to avoid bare message
		},
		{
			name: "thinking only content",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "some reasoning"},
				},
			},
			wantContentFull: true, // Should have " " to avoid bare message
		},
		{
			name: "tool calls with no text",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeToolUse, ToolUseID: "call_123", ToolName: "test_tool", ToolInput: json.RawMessage(`{"arg":"value"}`)},
				},
			},
			wantContentFull: false, // OK to have empty content when tool_calls is present
		},
		{
			name: "normal text content",
			msg: llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello, world!"},
				},
			},
			wantContentFull: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oaiMsgs := fromLLMMessage(tt.msg)
			if len(oaiMsgs) == 0 {
				t.Fatal("expected at least one message")
			}

			// Serialize to JSON to check what would be sent to the API
			for i, m := range oaiMsgs {
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatalf("failed to marshal message %d: %v", i, err)
				}

				// Parse back to check structure
				var parsed map[string]interface{}
				if err := json.Unmarshal(b, &parsed); err != nil {
					t.Fatalf("failed to unmarshal message %d: %v", i, err)
				}

				// Check that we don't have a bare {"role": "assistant"} message
				if m.Role == "assistant" {
					_, hasContent := parsed["content"]
					_, hasMultiContent := parsed["multi_content"]
					_, hasToolCalls := parsed["tool_calls"]
					_, hasReasoningContent := parsed["reasoning_content"]

					// At least one of these should be present to avoid bare assistant message
					if !hasContent && !hasMultiContent && !hasToolCalls && !hasReasoningContent {
						t.Errorf("bare assistant message detected: %s", string(b))
					}

					// Check specific expectations
					if tt.wantContentFull && !hasContent {
						t.Errorf("expected content field to be present, got: %s", string(b))
					}
				}
			}
		})
	}
}

// TestToolMessageNotBare verifies the existing fix for tool messages (line 758)
func TestToolMessageNotBare(t *testing.T) {
	msg := llm.Message{
		Role: llm.MessageRoleUser, // Tool results are sent as user messages with tool content
		Content: []llm.Content{
			{Type: llm.ContentTypeToolResult, ToolUseID: "call_123", ToolResult: []llm.Content{}},
		},
	}

	oaiMsgs := fromLLMMessage(msg)
	if len(oaiMsgs) == 0 {
		t.Fatal("expected at least one message")
	}

	for _, m := range oaiMsgs {
		if m.Role == "tool" {
			b, _ := json.Marshal(m)
			var parsed map[string]interface{}
			json.Unmarshal(b, &parsed)

			_, hasContent := parsed["content"]
			if !hasContent {
				t.Errorf("tool message should have content field even if empty, got: %s", string(b))
			}

			// Verify it's a space character
			if m.Content != " " {
				t.Errorf("expected content to be a space, got: %q", m.Content)
			}
		}
	}
}

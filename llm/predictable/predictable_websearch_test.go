package predictable

import (
	"context"
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

// TestPredictableWebSearchCitations verifies the "web search" predictable
// pattern reproduces the Anthropic server-side web-search shape: a
// server_tool_use block, a web_search_tool_result with sources, and a run of
// text blocks where cited quotes carry a Citations array. This is what the UI
// coalesces into flowing paragraphs with inline citation markers.
func TestPredictableWebSearchCitations(t *testing.T) {
	for _, trigger := range []string{"web search", "citations"} {
		t.Run(trigger, func(t *testing.T) {
			svc := NewService()
			req := &llm.Request{
				Messages: []llm.Message{{
					Role:    llm.MessageRoleUser,
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: trigger}},
				}},
			}
			resp, err := svc.Do(context.Background(), req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}

			var (
				serverToolUse int
				searchResults int
				textBlocks    int
				citedBlocks   int
			)
			for _, c := range resp.Content {
				switch c.Type {
				case llm.ContentTypeServerToolUse:
					serverToolUse++
				case llm.ContentTypeWebSearchToolResult:
					searchResults = len(c.ToolResult)
				case llm.ContentTypeText:
					textBlocks++
					if len(c.Citations) > 0 {
						citedBlocks++
						// Citations must be a valid JSON array of objects
						// with the web_search_result_location shape.
						var arr []map[string]any
						if err := json.Unmarshal(c.Citations, &arr); err != nil {
							t.Fatalf("citations not valid JSON: %v", err)
						}
						if len(arr) == 0 {
							t.Fatalf("empty citation array on a cited block")
						}
						if arr[0]["url"] == "" || arr[0]["type"] != "web_search_result_location" {
							t.Fatalf("unexpected citation shape: %v", arr[0])
						}
					}
				}
			}

			if serverToolUse != 1 {
				t.Errorf("server_tool_use blocks = %d, want 1", serverToolUse)
			}
			if searchResults == 0 {
				t.Errorf("web search results = 0, want > 0")
			}
			if textBlocks < 5 {
				t.Errorf("text blocks = %d, want several (to exercise coalescing)", textBlocks)
			}
			if citedBlocks == 0 {
				t.Errorf("cited text blocks = 0, want > 0")
			}
		})
	}
}

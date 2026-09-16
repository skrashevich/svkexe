package browse

import (
	"context"

	"shelley.exe.dev/llm"
)

// RegisterBrowserTools returns browser tools (combined browser tool + read_image) ready to be added to an agent.
// It also returns a cleanup function that should be called when done to properly close the browser.
// The browser will be initialized lazily when a browser tool is first used.
// Per-image size limits are looked up from the llm.Service in the tool call
// context at run time, not configured here.
func RegisterBrowserTools(ctx context.Context) ([]*llm.Tool, func()) {
	browserTools := NewBrowseTools(ctx, 0)

	return browserTools.GetTools(), func() {
		browserTools.Close()
	}
}

// Tool is an alias for llm.Tool to make the documentation clearer
type Tool = llm.Tool

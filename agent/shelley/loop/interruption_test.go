package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
)

// TestInterruptionDuringToolExecution tests that user messages queued during
// tool execution are processed after the tool completes but before the next
// tool starts (not at the end of the entire turn).
func TestInterruptionDuringToolExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Track when the tool is called and when it completes
		var toolStarted atomic.Bool
		var toolCompleted atomic.Bool
		var interruptionSeen atomic.Bool

		// The tool blocks until the test releases it, so "while the tool is
		// executing" is a window the test controls rather than a duration.
		release := make(chan struct{})
		slowTool := &llm.Tool{
			Name:        "slow_tool",
			Description: "A tool that takes time to execute",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"input": {"type": "string"}}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				toolStarted.Store(true)
				select {
				case <-release:
				case <-ctx.Done():
				}
				toolCompleted.Store(true)
				return llm.ToolOut{
					LLMContent: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Tool completed"},
					},
				}
			},
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		}

		// Create a service that detects the interruption
		service := &customService{
			responseFunc: func(req *llm.Request) (*llm.Response, error) {
				// Check if we've seen the interruption
				toolResults := 0
				for _, msg := range req.Messages {
					for _, c := range msg.Content {
						if c.Type == llm.ContentTypeToolResult {
							toolResults++
						}
						if c.Type == llm.ContentTypeText && c.Text == "INTERRUPTION" {
							interruptionSeen.Store(true)
							return &llm.Response{
								Role:       llm.MessageRoleAssistant,
								StopReason: llm.StopReasonEndTurn,
								Content: []llm.Content{
									{Type: llm.ContentTypeText, Text: "Acknowledged interruption"},
								},
							}, nil
						}
					}
				}

				// First call: use the slow tool
				if toolResults == 0 {
					return &llm.Response{
						Role:       llm.MessageRoleAssistant,
						StopReason: llm.StopReasonToolUse,
						Content: []llm.Content{
							{Type: llm.ContentTypeText, Text: "I'll use the slow tool"},
							{
								Type:      llm.ContentTypeToolUse,
								ID:        "tool_1",
								ToolName:  "slow_tool",
								ToolInput: json.RawMessage(`{"input":"test"}`),
							},
						},
					}, nil
				}

				// After tool result, continue with more work
				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonEndTurn,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Done with tool"},
					},
				}, nil
			},
		}

		loop := NewLoop(Config{
			LLM:           service,
			History:       []llm.Message{},
			Tools:         []*llm.Tool{slowTool},
			RecordMessage: recordMessage,
		})

		// Queue initial user message that will trigger tool use
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "use the tool"}},
		})

		// Run the loop in background
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var loopDone sync.WaitGroup
		loopDone.Go(func() {
			loop.Go(ctx)
		})

		// Let the loop run until it is parked inside the tool call.
		synctest.Wait()
		if !toolStarted.Load() {
			t.Fatal("tool never started")
		}

		// Queue an interruption message while tool is executing
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "INTERRUPTION"}},
		})

		// The message must remain in the queue while the tool is executing:
		// once everything is blocked again, nothing may have consumed it.
		synctest.Wait()
		if toolCompleted.Load() {
			t.Fatal("tool completed before it was released")
		}
		loop.mu.Lock()
		queueLen := len(loop.messageQueue)
		loop.mu.Unlock()
		if queueLen != 1 {
			t.Fatalf("queue length during tool execution = %d, want 1", queueLen)
		}

		// Release the tool and let the turn run to completion.
		close(release)
		synctest.Wait()
		cancel()
		loopDone.Wait()

		// Verify the interruption was seen by the LLM
		if !interruptionSeen.Load() {
			t.Error("Interruption was never seen by the LLM")
		}
	})
}

// TestInterruptionDuringMultiToolChain tests interruption during a chain of tool calls.
// With the fix, the interruption should be visible to the LLM after the first tool completes.
func TestInterruptionDuringMultiToolChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var toolCallCount atomic.Int32
		var interruptionSeenAtToolResult atomic.Int32 // -1 means not seen

		// The first tool call blocks until the test releases it, so the STOP
		// message is queued while that call is in flight.
		release := make(chan struct{})
		multiTool := &llm.Tool{
			Name:        "multi_tool",
			Description: "A tool that might be called multiple times",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"step": {"type": "integer"}}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				toolCallCount.Add(1)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return llm.ToolOut{
					LLMContent: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Tool step completed"},
					},
				}
			},
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		}

		// Service that makes multiple tool calls but stops when it sees "STOP"
		interruptionSeenAtToolResult.Store(-1)
		service := &customService{
			responseFunc: func(req *llm.Request) (*llm.Response, error) {
				// Check if we've seen the STOP message
				toolResults := 0
				for _, msg := range req.Messages {
					for _, c := range msg.Content {
						if c.Type == llm.ContentTypeToolResult {
							toolResults++
						}
						if c.Type == llm.ContentTypeText && c.Text == "STOP" {
							// Record when we first saw the interruption
							interruptionSeenAtToolResult.CompareAndSwap(-1, int32(toolResults))
							// Stop immediately when we see the interruption
							return &llm.Response{
								Role:       llm.MessageRoleAssistant,
								StopReason: llm.StopReasonEndTurn,
								Content: []llm.Content{
									{Type: llm.ContentTypeText, Text: "Stopped due to user interruption"},
								},
							}, nil
						}
					}
				}

				if toolResults < 5 {
					// Keep calling the tool (would do 5 if not interrupted)
					return &llm.Response{
						Role:       llm.MessageRoleAssistant,
						StopReason: llm.StopReasonToolUse,
						Content: []llm.Content{
							{Type: llm.ContentTypeText, Text: "Calling tool again"},
							{
								Type:      llm.ContentTypeToolUse,
								ID:        fmt.Sprintf("tool_%d", toolResults+1),
								ToolName:  "multi_tool",
								ToolInput: json.RawMessage(fmt.Sprintf(`{"step":%d}`, toolResults+1)),
							},
						},
					}, nil
				}

				// Done with tools
				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonEndTurn,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "All tools completed"},
					},
				}, nil
			},
		}

		loop := NewLoop(Config{
			LLM:           service,
			History:       []llm.Message{},
			Tools:         []*llm.Tool{multiTool},
			RecordMessage: recordMessage,
		})

		// Queue initial user message
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run the tool 5 times"}},
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var loopDone sync.WaitGroup
		loopDone.Go(func() {
			loop.Go(ctx)
		})

		// Let the loop run until it is parked inside the first tool call.
		synctest.Wait()
		if n := toolCallCount.Load(); n != 1 {
			t.Fatalf("tool call count before interruption = %d, want 1", n)
		}

		// Queue interruption while the first tool call is in flight, then
		// release the tool and let the loop run until it is idle.
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "STOP"}},
		})
		close(release)
		synctest.Wait()

		cancel()
		loopDone.Wait()

		finalToolCount := toolCallCount.Load()
		seenAt := interruptionSeenAtToolResult.Load()

		// The interruption must be seen right after the tool result of the
		// call that was running when STOP was queued, not at the end of the
		// chain.
		switch {
		case seenAt == -1:
			t.Error("Interruption was never seen by the LLM")
		case seenAt != 1:
			t.Errorf("Interruption was delayed: seen after %d tool results, expected 1", seenAt)
		}

		// Seeing the interruption ends the turn, so no further tool calls
		// may follow it.
		if finalToolCount != 1 {
			t.Errorf("tool call count = %d, want 1: interruption should have stopped the chain", finalToolCount)
		}
	})
}

// customService allows custom response logic for testing
type customService struct {
	responses    []customResponse
	responseFunc func(req *llm.Request) (*llm.Response, error)
	callIndex    int
	mu           sync.Mutex
}

type customResponse struct {
	response *llm.Response
	err      error
}

func (s *customService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.responseFunc != nil {
		return s.responseFunc(req)
	}

	if s.callIndex >= len(s.responses) {
		// Default response
		return &llm.Response{
			Role:       llm.MessageRoleAssistant,
			StopReason: llm.StopReasonEndTurn,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "No more responses configured"},
			},
		}, nil
	}

	resp := s.responses[s.callIndex]
	s.callIndex++
	return resp.response, resp.err
}

func (s *customService) GetDefaultModel() string {
	return "custom-test"
}

func (s *customService) Provider() string { return "" }

func (s *customService) TokenContextWindow() int {
	return 100000
}

func (s *customService) MaxImageDimension() int {
	return 8000
}

func (s *customService) MaxImageBytes() int {
	return 0
}

// TestNoInterruptionNormalFlow verifies that normal tool chains work correctly
// when no interruption is queued.
func TestNoInterruptionNormalFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var toolCallCount atomic.Int32

		// Create a tool that tracks calls
		multiTool := &llm.Tool{
			Name:        "multi_tool",
			Description: "A tool",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"step": {"type": "integer"}}}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				toolCallCount.Add(1)
				return llm.ToolOut{
					LLMContent: []llm.Content{
						{Type: llm.ContentTypeText, Text: "done"},
					},
				}
			},
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage, otherUsage []llm.PurposedUsage) error {
			return nil
		}

		// Service that makes 3 tool calls then finishes
		service := &customService{
			responseFunc: func(req *llm.Request) (*llm.Response, error) {
				toolResults := 0
				for _, msg := range req.Messages {
					for _, c := range msg.Content {
						if c.Type == llm.ContentTypeToolResult {
							toolResults++
						}
					}
				}

				if toolResults < 3 {
					return &llm.Response{
						Role:       llm.MessageRoleAssistant,
						StopReason: llm.StopReasonToolUse,
						Content: []llm.Content{
							{Type: llm.ContentTypeText, Text: "Calling tool"},
							{
								Type:      llm.ContentTypeToolUse,
								ID:        fmt.Sprintf("tool_%d", toolResults+1),
								ToolName:  "multi_tool",
								ToolInput: json.RawMessage(fmt.Sprintf(`{"step":%d}`, toolResults+1)),
							},
						},
					}, nil
				}

				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonEndTurn,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "All done"},
					},
				}, nil
			},
		}

		loop := NewLoop(Config{
			LLM:           service,
			History:       []llm.Message{},
			Tools:         []*llm.Tool{multiTool},
			RecordMessage: recordMessage,
		})

		// Queue initial user message (no interruption)
		loop.QueueUserMessage(llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run tools"}},
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var loopDone sync.WaitGroup
		loopDone.Go(func() {
			loop.Go(ctx)
		})

		// Let the turn run until the loop is idle.
		synctest.Wait()
		cancel()
		loopDone.Wait()

		if finalCount := toolCallCount.Load(); finalCount != 3 {
			t.Errorf("Expected 3 tool calls, got %d", finalCount)
		}
	})
}

func (s *customService) SupportsImages() bool { return true }

package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"shelley.exe.dev/llm"
)

const picoMaxIterations = 1024

// errTurnEnded is how a round reports that it already closed the turn itself:
// a truncated or refused response has been recorded with its explanatory error
// message, and there is nothing further to ask the model. Without it a round
// that returns no response reads as an empty turn and is retried.
var errTurnEnded = errors.New("turn ended by the loop")

// picoEmptyTurnRetries bounds how often a turn that produced nothing is asked
// again before the user is told why their task stopped.
const picoEmptyTurnRetries = 2

// isEmptyTurn reports whether a response carries nothing the turn can continue
// from. Reasoning alone does not count: it is never shown as an answer and
// never executed, so a turn that ends there has silently stalled.
func isEmptyTurn(resp *llm.Response) bool {
	if resp == nil {
		return true
	}
	if resp.StopReason == llm.StopReasonToolUse {
		return false
	}
	for _, c := range resp.Content {
		switch c.Type {
		case llm.ContentTypeToolUse:
			return false
		case llm.ContentTypeText:
			if strings.TrimSpace(c.Text) != "" {
				return false
			}
		}
	}
	return true
}

// PicoClaw owns iteration and parallel dispatch. The provider bridge retains
// Shelley's lossless history, system prompts, streaming, usage and persistence;
// flattening those into PicoClaw's text messages would lose images, thinking
// signatures, tool widgets and interrupted-turn bookkeeping.
func (l *Loop) runPicoClaw(ctx context.Context, round func(context.Context) (*llm.Response, error)) error {
	registry := tools.NewToolRegistry()
	driver := &picoDriver{loop: l, round: round, registry: registry}
	result, err := tools.RunToolLoop(ctx, tools.ToolLoopConfig{
		Provider: driver, Tools: registry, MaxIterations: picoMaxIterations,
	}, nil, "picoclaw", "")
	// Also publish the final batch on cancellation or iteration exhaustion.
	if publishErr := driver.publish(ctx); publishErr != nil {
		return publishErr
	}
	if errors.Is(err, errMessagePersistence) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if result.Iterations == picoMaxIterations && !driver.done {
		err := fmt.Errorf("PicoClaw reached the limit of %d tool rounds; ask the agent to continue", picoMaxIterations)
		if recordErr := l.recordMessage(ctx, llm.Message{
			Role: llm.MessageRoleAssistant, Content: llm.TextContent(err.Error()),
			EndOfTurn: true, ErrorType: llm.ErrorTypeLLMRequest,
		}, llm.Usage{}, nil); recordErr != nil {
			return fmt.Errorf("%w: %v", errMessagePersistence, recordErr)
		}
		return err
	}
	return nil
}

type picoDriver struct {
	loop     *Loop
	round    func(context.Context) (*llm.Response, error)
	registry *tools.ToolRegistry
	batch    *picoBatch
	usage    llm.UsageAccumulator
	done     bool
}

func (*picoDriver) GetDefaultModel() string { return "picoclaw" }
func (p *picoDriver) publish(ctx context.Context) error {
	if p.batch == nil {
		return nil
	}
	batch := p.batch
	p.batch = nil
	return p.loop.publishToolResults(ctx, batch.results, p.usage.Take())
}

func (p *picoDriver) Chat(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	if err := p.publish(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := p.round(ctx)
	if errors.Is(err, errTurnEnded) {
		p.done = true
		return &providers.LLMResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	// A turn that ends with nothing to show is not an answer: the model either
	// produced only reasoning or wrote its tool call as prose instead of a real
	// call, which weaker models do. Treating that as a finished turn abandons
	// the task mid-way with an empty message. Ask again — a retry usually draws
	// a model that can call tools — and only give up with a reason that names
	// the cause.
	for retries := 0; isEmptyTurn(resp) && err == nil; retries++ {
		if retries == picoEmptyTurnRetries {
			return nil, fmt.Errorf("the model returned neither an answer nor a tool call %d times; it may not support tool calling — switch this VM to a model that does", retries+1)
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		resp, err = p.round(ctx)
	}
	if errors.Is(err, errTurnEnded) {
		// The retried round ran into a truncation or refusal instead.
		p.done = true
		return &providers.LLMResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.StopReason != llm.StopReasonToolUse {
		p.done = true
		return &providers.LLMResponse{}, nil
	}
	var calls []llm.Content
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeToolUse {
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return nil, fmt.Errorf("model requested tool use without tool calls")
	}
	p.batch = newPicoBatch(p.loop, calls, &p.usage)
	return &providers.LLMResponse{ToolCalls: p.batch.register(p.registry)}, nil
}

// Per-call registrations carry original IDs and raw JSON without converting
// numbers to float64 or adding internal arguments to the model's tool schema.
// The private dispatch names never enter the LLM request, history or web UI.
type picoBatch struct {
	loop    *Loop
	calls   []llm.Content
	results []llm.Content
	usage   *llm.UsageAccumulator
	ready   sync.WaitGroup
	release chan struct{}
	once    sync.Once
	run     bool
}

func newPicoBatch(l *Loop, calls []llm.Content, usage *llm.UsageAccumulator) *picoBatch {
	b := &picoBatch{loop: l, calls: calls, results: make([]llm.Content, len(calls)), usage: usage, release: make(chan struct{})}
	b.ready.Add(len(calls))
	return b
}
func (b *picoBatch) register(r *tools.ToolRegistry) []providers.ToolCall {
	calls := make([]providers.ToolCall, len(b.calls))
	for i, call := range b.calls {
		tool := &picoCall{batch: b, index: i}
		r.Register(tool)
		calls[i] = providers.ToolCall{ID: call.ID, Name: tool.Name(), Arguments: map[string]any{}}
	}
	return calls
}

type picoCall struct {
	batch *picoBatch
	index int
}

func (t *picoCall) Name() string      { return fmt.Sprintf("picoclaw_call_%d", t.index) }
func (*picoCall) Description() string { return "Execute the original coding tool call" }
func (*picoCall) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *picoCall) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	b := t.batch
	b.ready.Done()
	b.once.Do(func() { b.ready.Wait(); b.run = ctx.Err() == nil; close(b.release) })
	<-b.release
	call := b.calls[t.index]
	if !b.run {
		b.results[t.index] = llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: call.ID, ToolError: true, ToolResult: llm.TextContent(notExecutedToolResultText)}
	} else {
		ctx = llm.WithUsageCollector(ctx, b.usage.Collect)
		b.results[t.index] = b.loop.executeToolCall(ctx, call)
	}
	// Rich results are published in request order before the next provider call.
	// PicoClaw needs only an acknowledgement, never a lossy second history.
	return &tools.ToolResult{ForLLM: "Tool result recorded"}
}

// Used by the existing batch contract tests and callers. Dispatch remains in
// PicoClaw, including the cancellation start barrier and result ordering.
func (l *Loop) picoExecuteBatch(ctx context.Context, calls []llm.Content) []llm.Content {
	var usage llm.UsageAccumulator
	b := newPicoBatch(l, calls, &usage)
	r := tools.NewToolRegistry()
	response := &providers.LLMResponse{ToolCalls: b.register(r)}
	p := &picoBatchProvider{response: response}
	_, _ = tools.RunToolLoop(ctx, tools.ToolLoopConfig{Provider: p, Tools: r, MaxIterations: 1}, nil, "picoclaw", "")
	if collector := llm.UsageCollectorFromContext(ctx); collector != nil {
		for _, u := range usage.Take() {
			collector(u.Purpose, u.Usage)
		}
	}
	return b.results
}

type picoBatchProvider struct{ response *providers.LLMResponse }

func (*picoBatchProvider) GetDefaultModel() string { return "picoclaw" }
func (p *picoBatchProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	return p.response, nil
}

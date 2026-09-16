package llm

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestRequestTrace(t *testing.T) {
	ctx, trace := WithRequestTrace(context.Background())
	if RequestTraceFromContext(ctx) != trace {
		t.Fatal("RequestTraceFromContext did not return attached trace")
	}
	trace.Set("", "ignored")
	trace.Set("ignored", "")
	trace.Set("shelley_request_id", "local-1")
	trace.Set("upstream_request_id", "upstream-1")
	trace.Set("shelley_request_id", "local-2")
	if got := trace.Value("shelley_request_id"); got != "local-2" {
		t.Fatalf("Value = %q, want local-2", got)
	}
	if got := trace.String(); got != "shelley_request_id=local-2 upstream_request_id=upstream-1" {
		t.Fatalf("String = %q", got)
	}
}

func TestRequestTraceConcurrent(t *testing.T) {
	var trace RequestTrace
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("field_%02d", i)
			trace.Set(name, name)
			if got := trace.Value(name); got != name {
				t.Errorf("Value(%q) = %q", name, got)
			}
		}()
	}
	wg.Wait()
	if got := trace.String(); got == "" {
		t.Fatal("String returned empty diagnostics")
	}
}

func TestIndirectUsageContext(t *testing.T) {
	var accumulator UsageAccumulator
	ctx := WithUsageCollector(context.Background(), accumulator.Collect)
	ctx = WithPurpose(ctx, "keyword_search")
	collector := UsageCollectorFromContext(ctx)
	if collector == nil {
		t.Fatal("UsageCollectorFromContext returned nil")
	}
	collector(PurposeFromContext(ctx), Usage{InputTokens: 4, OutputTokens: 2})
	entries := accumulator.Take()
	if len(entries) != 1 || entries[0].Purpose != "keyword_search" || entries[0].Usage.InputTokens != 4 || entries[0].Usage.OutputTokens != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries := accumulator.Take(); len(entries) != 0 {
		t.Fatalf("Take did not reset accumulator: %+v", entries)
	}
}

func TestUsageAccumulatorConcurrent(t *testing.T) {
	var accumulator UsageAccumulator
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			accumulator.Collect(fmt.Sprintf("purpose_%02d", i), Usage{InputTokens: 1})
		}()
	}
	wg.Wait()
	entries := accumulator.Take()
	if len(entries) != 20 {
		t.Fatalf("len(Take()) = %d, want 20", len(entries))
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDebugHistograms verifies the histograms endpoint aggregates real
// conversation data and serves both JSON and an HTML page.
func TestDebugHistograms(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	ctx := context.Background()

	// Seed two conversations of different sizes (the second compacts).
	if _, err := server.generateLoremConversation(ctx, 5, "claude-opus-4-5"); err != nil {
		t.Fatalf("generate small: %v", err)
	}
	if _, err := server.generateLoremConversation(ctx, 100, "claude-opus-4-5"); err != nil {
		t.Fatalf("generate large: %v", err)
	}

	// JSON output.
	req := httptest.NewRequest("GET", "/debug/histograms?json=1", nil)
	w := httptest.NewRecorder()
	server.handleDebugHistograms(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("json status = %d: %s", w.Code, w.Body.String())
	}
	var st histogramStats
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Conversations < 2 {
		t.Errorf("expected >=2 conversations, got %d", st.Conversations)
	}
	if st.Messages == 0 || st.Bytes == 0 {
		t.Errorf("expected nonzero messages/bytes, got %d/%d", st.Messages, st.Bytes)
	}
	if len(st.MessagesPerConv) != st.Conversations {
		t.Errorf("messages_per_conv len %d != conversations %d", len(st.MessagesPerConv), st.Conversations)
	}
	if st.MessagesPercentiles.Max < st.MessagesPercentiles.P50 {
		t.Errorf("max %d < p50 %d", st.MessagesPercentiles.Max, st.MessagesPercentiles.P50)
	}
	// The 100-turn conversation compacts, so at least one conversation must
	// have generation > 1. Generation labels are numeric strings.
	sawHigherGen := false
	var genConvs int64
	for _, g := range st.GenerationCounts {
		genConvs += g.Count
		if g.Label != "1" {
			sawHigherGen = true
		}
	}
	if !sawHigherGen {
		t.Error("expected at least one conversation past generation 1")
	}
	if int(genConvs) != st.Conversations {
		t.Errorf("generation counts sum %d != conversations %d", genConvs, st.Conversations)
	}
	if len(st.TypeCounts) == 0 {
		t.Error("expected message-type counts")
	}

	// HTML output embeds the stats blob.
	req = httptest.NewRequest("GET", "/debug/histograms", nil)
	w = httptest.NewRecorder()
	server.handleDebugHistograms(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("html status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "__STATS_JSON__") {
		t.Error("stats placeholder was not substituted")
	}
	if !strings.Contains(body, "vega-lite") || !strings.Contains(body, "messages_per_conv") {
		t.Error("html missing vega-lite or stats data")
	}
}

// TestComputePercentiles checks the percentile helper on known inputs.
func TestComputePercentiles(t *testing.T) {
	t.Parallel()
	if got := computePercentiles(nil); got != (percentiles{}) {
		t.Errorf("empty sample: got %+v", got)
	}
	sample := []int64{}
	for i := int64(1); i <= 100; i++ {
		sample = append(sample, i)
	}
	p := computePercentiles(sample)
	if p.Min != 1 || p.Max != 100 {
		t.Errorf("min/max: got %d/%d", p.Min, p.Max)
	}
	if p.P50 != 50 || p.P90 != 90 || p.P99 != 99 {
		t.Errorf("percentiles: p50=%d p90=%d p99=%d", p.P50, p.P90, p.P99)
	}
	if p.Mean != 50.5 {
		t.Errorf("mean: got %v want 50.5", p.Mean)
	}
}

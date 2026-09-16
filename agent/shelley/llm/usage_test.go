package llm

import (
	"encoding/json"
	"testing"
)

func TestUsageTotalInputTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  uint64
	}{
		{
			name: "all token types",
			usage: Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     200,
				OutputTokens:             30,
			},
			want: 350, // 100 + 50 + 200
		},
		{
			name: "only input tokens",
			usage: Usage{
				InputTokens:  150,
				OutputTokens: 50,
			},
			want: 150,
		},
		{
			name: "heavy caching",
			usage: Usage{
				InputTokens:              10,
				CacheCreationInputTokens: 0,
				CacheReadInputTokens:     5000,
				OutputTokens:             100,
			},
			want: 5010, // 10 + 0 + 5000
		},
		{
			name:  "zero",
			usage: Usage{},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.TotalInputTokens()
			if got != tt.want {
				t.Errorf("TotalInputTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUsageContextWindowUsed(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  uint64
	}{
		{
			name: "all token types",
			usage: Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     200,
				OutputTokens:             30,
			},
			want: 380, // 100 + 50 + 200 + 30
		},
		{
			name: "only input and output",
			usage: Usage{
				InputTokens:  150,
				OutputTokens: 50,
			},
			want: 200,
		},
		{
			name: "heavy caching with output",
			usage: Usage{
				InputTokens:              10,
				CacheCreationInputTokens: 0,
				CacheReadInputTokens:     5000,
				OutputTokens:             100,
			},
			want: 5110, // 10 + 0 + 5000 + 100
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.ContextWindowUsed()
			if got != tt.want {
				t.Errorf("ContextWindowUsed() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPurposedUsageJSONInlinesUsageFields(t *testing.T) {
	data, err := json.Marshal(PurposedUsage{
		Purpose: "compaction",
		Usage:   Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.01, Model: "m", URL: "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// The embedded Usage's fields must be inlined, not nested under "Usage".
	if m["purpose"] != "compaction" || m["input_tokens"] != float64(100) || m["model"] != "m" || m["url"] != "u" {
		t.Errorf("unexpected JSON shape: %s", data)
	}
	if _, nested := m["Usage"]; nested {
		t.Errorf("Usage fields nested instead of inlined: %s", data)
	}

	var back PurposedUsage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Purpose != "compaction" || back.InputTokens != 100 || back.CostUSD != 0.01 {
		t.Errorf("round-trip = %+v", back)
	}
}

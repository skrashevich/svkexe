package picoclaw

import (
	"strings"
	"testing"
)

func TestSQLQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", "'plain'"},
		{"it's", "'it''s'"},
		{"openai/gpt-oss-120b:free", "'openai/gpt-oss-120b:free'"},
	}
	for _, tc := range tests {
		if got := sqlQuote(tc.in); got != tc.want {
			t.Fatalf("sqlQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildSeedSQL(t *testing.T) {
	sql := buildSeedSQL(
		[]string{"openai/gpt-oss-120b:free", "z-ai/glm-4.5-air:free"},
		"https://svk.bar/api/llm/v1",
		"secret-token",
	)
	if !strings.Contains(sql, "BEGIN IMMEDIATE;") {
		t.Fatalf("missing transaction begin: %s", sql)
	}
	if !strings.Contains(sql, "DELETE FROM models WHERE model_id LIKE 'svkexe-%';") {
		t.Fatalf("missing cleanup of stale svkexe models: %s", sql)
	}
	if !strings.Contains(sql, "svkexe-openai/gpt-oss-120b:free") {
		t.Fatalf("missing first model id: %s", sql)
	}
	if !strings.Contains(sql, "'https://svk.bar/api/llm/v1'") {
		t.Fatalf("missing quoted base URL: %s", sql)
	}
	if !strings.Contains(sql, "'secret-token'") {
		t.Fatalf("missing quoted token: %s", sql)
	}
	if strings.Contains(sql, "svkexe-, ") {
		t.Fatalf("broken SQL from special characters: %s", sql)
	}
	if !strings.Contains(sql, "COMMIT;") {
		t.Fatalf("missing transaction commit: %s", sql)
	}
}

func TestExpectedSeedModelIDs(t *testing.T) {
	got := expectedSeedModelIDs([]string{
		"z-ai/glm-4.5-air:free",
		"openai/gpt-oss-120b:free",
	})
	want := []string{
		"svkexe-openai/gpt-oss-120b:free",
		"svkexe-z-ai/glm-4.5-air:free",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expectedSeedModelIDs = %v, want %v", got, want)
	}
}

package llmproxy

import "testing"

func TestValidateConfig_RequiresToken(t *testing.T) {
	err := ValidateConfig(Config{APIKey: "sk-or-test", InternalToken: ""})
	if err == nil {
		t.Fatal("expected error when API key set without internal token")
	}
}

func TestValidateConfig_OkWithToken(t *testing.T) {
	if err := ValidateConfig(Config{APIKey: "sk-or-test", InternalToken: "secret"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

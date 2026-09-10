package db

import "testing"

func TestProviderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, provider, url, models, key string
		valid                            bool
	}{
		{"router", "openrouter", "", "vendor/model", "key", true},
		{"local", "custom-local", "http://localhost:8000/v1", "model", "", true},
		{"legacy", "anthropic", "", "", "key", true},
		{"missing URL", "custom-local", "", "model", "key", false},
		{"missing models", "openrouter", "", "", "key", false},
		{"bad protocol", "custom-local", "file:///tmp/models", "model", "key", false},
		{"credentials", "custom-local", "https://user:pass@host/v1", "model", "key", false},
		{"query", "custom-local", "https://host/v1?token=secret", "model", "key", false},
		{"env injection", "openai", "", "", "key\nEVIL=value", false},
		{"provider injection", "custom-a=b", "https://host/v1", "model", "key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NormalizeProvider(tc.provider, tc.url, tc.models, tc.key)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestProviderMigrationPreservesKeys(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("old", "old@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateAPIKey("old-key", "old", "openai", "secret", testEncKey); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"base_url", "models"} {
		if _, err := database.Exec("ALTER TABLE api_keys DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := database.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := database.ListAPIKeysByOwner("old")
	if err != nil || len(keys) != 1 || keys[0].BaseURL != "" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	plain, err := database.GetAPIKeyPlaintext("old-key", testEncKey)
	if err != nil || plain != "secret" {
		t.Fatal("migration lost key")
	}
}

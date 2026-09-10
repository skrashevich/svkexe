package picoclaw

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/secrets"
)

// A VM created before the owner had keys runs on a gateway model from the
// deployment-wide list. Once they add their own key, the VM must move to it:
// the gateway list is shared and may name models this account cannot reach.
func TestRefreshProviderKeysAdoptsOwnerDefaultModel(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	enc := []byte("01234567890123456789012345678901")
	if err := database.SaveProviderKey("key", owner.ID, "openrouter", "secret", "https://host/api/v1", "openrouter/free,openrouter/other", enc); err != nil {
		t.Fatal(err)
	}
	m := secrets.NewMaterializer(database, enc, t.TempDir())
	guest := &guestRuntime{files: map[string][]byte{
		ConfigFilePath: []byte(`{"default_model":"svkexe-cohere/north-mini-code:free"}`),
	}}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", owner.ID); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != "svkexe_user:openrouter:openrouter/free" {
		t.Fatalf("default model = %q, want the owner's own model", got)
	}

	// A model of theirs that still exists is their own choice and stays put.
	guest.files[ConfigFilePath] = []byte(`{"default_model":"svkexe_user:openrouter:openrouter/other"}`)
	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", owner.ID); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != "svkexe_user:openrouter:openrouter/other" {
		t.Fatalf("default model = %q, want the owner's choice preserved", got)
	}

	// The rewritten config must stay readable by the agent's unprivileged user.
	if script := strings.Join(guest.commands, "\n"); !strings.Contains(script, "chmod 640 "+EnvFilePath+" "+ConfigFilePath) {
		t.Errorf("config permissions not restored: %s", script)
	}
}

func guestDefaultModel(t *testing.T, guest *guestRuntime) string {
	t.Helper()
	var cfg map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg["default_model"]
}

func TestProviderModelsSync(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	enc := []byte("01234567890123456789012345678901")
	key := "key'$(must-not-execute)"
	if err := database.SaveProviderKey("key", owner.ID, "custom-test", key, "https://host/api/v1", "vendor/model", enc); err != nil {
		t.Fatal(err)
	}
	m := secrets.NewMaterializer(database, enc, t.TempDir())
	guest := &guestRuntime{files: map[string][]byte{}}
	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", owner.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(guest.commands, "\n"), key) {
		t.Fatal("secret entered shell code")
	}
	local, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	_, err = local.Exec(`CREATE TABLE models (model_id TEXT PRIMARY KEY, display_name TEXT, provider_type TEXT, endpoint TEXT, api_key TEXT, model_name TEXT, max_tokens INTEGER); INSERT INTO models (model_id) VALUES ('svkexe-global'), ('user-created');`)
	if err != nil {
		t.Fatal(err)
	}
	apply := func() {
		t.Helper()
		if _, err := local.Exec(string(guest.files[ConfigDir+"/provider-models.sql"])); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	var endpoint, gotKey, model string
	if err := local.QueryRow(`SELECT endpoint, api_key, model_name FROM models WHERE model_id = 'svkexe_user:custom-test:vendor/model'`).Scan(&endpoint, &gotKey, &model); err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://host/api/v1" || gotKey != key || model != "vendor/model" {
		t.Fatalf("incorrect connection")
	}
	if err := database.DeleteAPIKey("key"); err != nil {
		t.Fatal(err)
	}
	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", owner.ID); err != nil {
		t.Fatal(err)
	}
	apply()
	var count int
	if err := local.QueryRow(`SELECT count(*) FROM models`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("deleted unrelated models: %d, %v", count, err)
	}
	if len(guest.files[EnvFilePath]) != 0 {
		t.Fatal("stale credentials")
	}
	guest.fail = "sqlite3 -bail"
	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", owner.ID); err == nil {
		t.Fatal("sync failure hidden")
	}
}

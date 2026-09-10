package picoclaw

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/secrets"
)

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

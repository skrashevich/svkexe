package picoclaw

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/secrets"
)

func TestModelSeedWaitsForDatabaseWriter(t *testing.T) {
	for name, script := range map[string]string{
		"gateway":  buildSeedSQL([]string{"model"}, "https://host/v1", "key"),
		"provider": providerModelsSQL([]secrets.ProviderModel{{Provider: "custom-test", Model: "model", BaseURL: "https://host/v1", Key: "key"}}),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.db")
			holder, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			_, err = holder.Exec(`CREATE TABLE models (model_id TEXT PRIMARY KEY, display_name TEXT, provider_type TEXT, endpoint TEXT, api_key TEXT, model_name TEXT, max_tokens INTEGER)`)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := holder.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO models (model_id) VALUES ('existing')`); err != nil {
				t.Fatal(err)
			}
			writer, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			done := make(chan error, 1)
			go func() { _, err := writer.ExecContext(t.Context(), script); done <- err }()
			select {
			case err := <-done:
				t.Fatalf("seed did not wait for concurrent writer: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var count int
			if err := writer.QueryRow(`SELECT count(*) FROM models`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

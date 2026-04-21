package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/svkexe/platform/internal/db"
)

func setupTestDB(t *testing.T) (*db.DB, []byte) {
	t.Helper()
	tmpDir := t.TempDir()
	database, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// 32-byte AES-256 key
	encKey := make([]byte, 32)
	copy(encKey, []byte("test-encryption-key-for-testing!"))
	return database, encKey
}

func createTestUser(t *testing.T, database *db.DB, id string) {
	t.Helper()
	if _, err := database.EnsureUser(id, id+"@test.example"); err != nil {
		t.Fatalf("ensure user %s: %v", id, err)
	}
}

func TestMaterializeKeys(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()
	createTestUser(t, database, "owner-1")

	// Insert a test API key.
	if err := database.CreateAPIKey("key-1", "owner-1", "openai", "sk-test-value", encKey); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	m := NewMaterializer(database, encKey, basePath)
	if err := m.MaterializeKeys("container-1", "owner-1"); err != nil {
		t.Fatalf("MaterializeKeys: %v", err)
	}

	envFile := filepath.Join(basePath, "container-1", "env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}

	content := string(data)
	if content != "OPENAI_API_KEY=sk-test-value\n" {
		t.Errorf("unexpected env content: %q", content)
	}
}

func TestMaterializeKeys_FilePermissions(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()
	createTestUser(t, database, "owner-2")

	if err := database.CreateAPIKey("key-2", "owner-2", "anthropic", "sk-ant-test", encKey); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	m := NewMaterializer(database, encKey, basePath)
	if err := m.MaterializeKeys("container-2", "owner-2"); err != nil {
		t.Fatalf("MaterializeKeys: %v", err)
	}

	dir := filepath.Join(basePath, "container-2")
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Errorf("dir permissions: want 0700, got %o", dirInfo.Mode().Perm())
	}

	envFile := filepath.Join(dir, "env")
	fileInfo, err := os.Stat(envFile)
	if err != nil {
		t.Fatalf("stat env file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0400 {
		t.Errorf("file permissions: want 0400, got %o", fileInfo.Mode().Perm())
	}
}

func TestMaterializeKeys_NoKeys(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()

	m := NewMaterializer(database, encKey, basePath)
	if err := m.MaterializeKeys("container-empty", "owner-nokeys"); err != nil {
		t.Fatalf("MaterializeKeys with no keys: %v", err)
	}

	envFile := filepath.Join(basePath, "container-empty", "env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty env file, got %q", string(data))
	}
}

func TestRemoveKeys(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()
	createTestUser(t, database, "owner-3")

	if err := database.CreateAPIKey("key-3", "owner-3", "openai", "sk-remove-test", encKey); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	m := NewMaterializer(database, encKey, basePath)
	if err := m.MaterializeKeys("container-3", "owner-3"); err != nil {
		t.Fatalf("MaterializeKeys: %v", err)
	}

	// Verify dir exists.
	dir := filepath.Join(basePath, "container-3")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir should exist after MaterializeKeys: %v", err)
	}

	if err := m.RemoveKeys("container-3"); err != nil {
		t.Fatalf("RemoveKeys: %v", err)
	}

	// Dir should be gone.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should not exist after RemoveKeys")
	}
}

func TestRemoveKeys_NonExistent(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()

	m := NewMaterializer(database, encKey, basePath)
	// Should not error when directory doesn't exist.
	if err := m.RemoveKeys("nonexistent-container"); err != nil {
		t.Fatalf("RemoveKeys on nonexistent dir: %v", err)
	}
}

func TestRefreshKeys(t *testing.T) {
	database, encKey := setupTestDB(t)
	basePath := t.TempDir()
	createTestUser(t, database, "owner-4")

	if err := database.CreateAPIKey("key-4", "owner-4", "openai", "sk-original", encKey); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	m := NewMaterializer(database, encKey, basePath)
	if err := m.MaterializeKeys("container-4", "owner-4"); err != nil {
		t.Fatalf("MaterializeKeys: %v", err)
	}

	// Delete old key and add new one to simulate key rotation.
	if err := database.DeleteAPIKey("key-4"); err != nil {
		t.Fatalf("delete api key: %v", err)
	}
	if err := database.CreateAPIKey("key-4b", "owner-4", "openai", "sk-rotated", encKey); err != nil {
		t.Fatalf("create new api key: %v", err)
	}

	if err := m.RefreshKeys("container-4", "owner-4"); err != nil {
		t.Fatalf("RefreshKeys: %v", err)
	}

	envFile := filepath.Join(basePath, "container-4", "env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if string(data) != "OPENAI_API_KEY=sk-rotated\n" {
		t.Errorf("unexpected env content after refresh: %q", string(data))
	}
}

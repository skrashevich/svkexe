package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skrashevich/svkexe/internal/db"
)

// Materializer writes decrypted LLM keys to env files for containers.
type Materializer struct {
	db       *db.DB
	encKey   []byte
	basePath string
}

// NewMaterializer creates a Materializer that stores env files under basePath.
func NewMaterializer(database *db.DB, encKey []byte, basePath string) *Materializer {
	return &Materializer{
		db:       database,
		encKey:   encKey,
		basePath: basePath,
	}
}

// MaterializeKeys decrypts all keys for ownerID and writes them to
// {basePath}/{containerID}/env as KEY=value lines. Dir is 0700, file is 0400.
func (m *Materializer) MaterializeKeys(containerID, ownerID string) error {
	keys, err := m.db.ListAPIKeysByOwner(ownerID)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}

	dir := filepath.Join(m.basePath, containerID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create key dir: %w", err)
	}

	var sb strings.Builder
	for _, k := range keys {
		if k.BaseURL != "" {
			// An endpoint-backed connection reaches the agent as a protected DB
			// credential, not an environment variable. Skipping before the
			// decrypt keeps the plaintext from being materialised at all.
			continue
		}
		plaintext, err := m.db.GetAPIKeyPlaintext(k.ID, m.encKey)
		if err != nil {
			return fmt.Errorf("decrypt key %s: %w", k.ID, err)
		}
		// Use provider name uppercased as env var name, e.g. OPENAI_API_KEY.
		envName := strings.ToUpper(k.Provider) + "_API_KEY"
		sb.WriteString(envName)
		sb.WriteByte('=')
		sb.WriteString(plaintext)
		sb.WriteByte('\n')
	}

	envFile := filepath.Join(dir, "env")
	// Replace atomically: the existing file is deliberately read-only, so a
	// second setup cannot open it for writing as the unprivileged gateway user.
	f, err := os.CreateTemp(dir, ".env-*")
	if err != nil {
		return fmt.Errorf("create env file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(sb.String()); err != nil {
		f.Close()
		return fmt.Errorf("write env file: %w", err)
	}
	if err := f.Chmod(0400); err != nil {
		f.Close()
		return fmt.Errorf("protect env file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close env file: %w", err)
	}
	if err := os.Rename(f.Name(), envFile); err != nil {
		return fmt.Errorf("replace env file: %w", err)
	}
	return nil
}

// ReadKeys returns the content of the materialized env file for containerID.
func (m *Materializer) ReadKeys(containerID string) ([]byte, error) {
	envFile := filepath.Join(m.basePath, containerID, "env")
	return os.ReadFile(envFile)
}

// RemoveKeys deletes the env file and its directory for containerID.
func (m *Materializer) RemoveKeys(containerID string) error {
	dir := filepath.Join(m.basePath, containerID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove key dir: %w", err)
	}
	return nil
}

// RefreshKeys atomically re-materializes keys for the given container.
func (m *Materializer) RefreshKeys(containerID, ownerID string) error {
	return m.MaterializeKeys(containerID, ownerID)
}

// ProviderModels returns decrypted connection settings for DB-backed models.
// These values must only be written to the owner's protected guest configuration.
func (m *Materializer) ProviderModels(owner string) ([]ProviderModel, error) {
	keys, err := m.db.ListAPIKeysByOwner(owner)
	if err != nil {
		return nil, err
	}
	var result []ProviderModel
	for _, k := range keys {
		if k.BaseURL == "" {
			continue
		}
		key, err := m.db.GetAPIKeyPlaintext(k.ID, m.encKey)
		if err != nil {
			return nil, err
		}
		// An endpoint always names its protocol: NormalizeProvider fills one in
		// on every write, and the migration backfilled the rows that predate it.
		for _, model := range strings.Split(k.Models, ",") {
			if model != "" {
				result = append(result, ProviderModel{Provider: k.Provider, Model: model, BaseURL: k.BaseURL, Key: key, Protocol: k.Protocol})
			}
		}
	}
	return result, nil
}

// ProviderModel is one model reached through a connection the owner configured.
// Protocol is the agent's provider_type for that endpoint.
type ProviderModel struct{ Provider, Model, BaseURL, Key, Protocol string }

// ID is the agent-side identifier this model is seeded under.
func (m ProviderModel) ID() string { return db.UserModelID(m.Provider, m.Model) }

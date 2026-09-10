package db

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var customProvider = regexp.MustCompile(`^custom-[a-z0-9][a-z0-9-]{0,47}$`)

func ValidProvider(provider string) bool {
	switch provider {
	case "openai", "anthropic", "gemini", "fireworks", "openrouter":
		return true
	}
	return customProvider.MatchString(provider)
}

// NormalizeProvider validates settings shared by the dashboard and REST API.
// BaseURL is the complete API prefix, including /v1 where required.
func NormalizeProvider(provider, baseURL, models, key string) (string, string, error) {
	if !ValidProvider(provider) {
		return "", "", fmt.Errorf("invalid provider")
	}
	if strings.ContainsAny(key, "\r\n\x00") {
		return "", "", fmt.Errorf("key must be a single line")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if provider == "openrouter" && baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(baseURL, "\r\n\x00") {
			return "", "", fmt.Errorf("base URL must be an HTTP(S) API prefix without credentials, query or fragment")
		}
	}
	if strings.HasPrefix(provider, "custom-") && baseURL == "" {
		return "", "", fmt.Errorf("custom provider requires a base URL")
	}
	if baseURL != "" && (provider == "anthropic" || provider == "gemini") {
		return "", "", fmt.Errorf("custom endpoints require an OpenAI-compatible provider")
	}
	names := strings.FieldsFunc(models, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	normalized := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, "\x00\t ") {
			return "", "", fmt.Errorf("model IDs must not contain whitespace")
		}
		if !seen[name] {
			normalized = append(normalized, name)
			seen[name] = true
		}
	}
	if baseURL != "" && len(normalized) == 0 {
		return "", "", fmt.Errorf("specify at least one model ID for this endpoint")
	}
	if baseURL == "" && len(normalized) > 0 {
		return "", "", fmt.Errorf("model IDs require a base URL")
	}
	if key == "" && !strings.HasPrefix(provider, "custom-") {
		return "", "", fmt.Errorf("key is required")
	}
	return baseURL, strings.Join(normalized, ","), nil
}

// SaveProviderKey replaces an owner's provider settings atomically.
func (db *DB) SaveProviderKey(id, owner, provider, key, baseURL, models string, encKey []byte) error {
	baseURL, models, err := NormalizeProvider(provider, baseURL, models, key)
	if err != nil {
		return err
	}
	encrypted, err := encryptAESGCM(encKey, []byte(key))
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM api_keys WHERE owner_id = ? AND lower(provider) = ?`, owner, provider); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO api_keys (id, owner_id, provider, encrypted_key, base_url, models) VALUES (?, ?, ?, ?, ?, ?)`, id, owner, provider, encrypted, baseURL, models); err != nil {
		return err
	}
	return tx.Commit()
}

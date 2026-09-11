package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var customProvider = regexp.MustCompile(`^custom-[a-z0-9][a-z0-9-]{0,47}$`)

// DefaultProtocol is the wire protocol assumed for an endpoint that does not
// name one. It is what every connection stored before protocols were
// configurable was seeded as, so leaving it implicit keeps those working.
const DefaultProtocol = "openai"

// protocols are the wire protocols the agent can speak to a custom endpoint.
// They are the agent's own provider_type values, because that is where they end
// up: a value the agent does not recognise leaves the model unusable with
// nothing in the dashboard to explain why.
//
// The distinction is not cosmetic. "openai" posts to {base}/chat/completions
// and "openai-responses" to {base}/responses, and a gateway commonly serves one
// and not the other — api.openmodel.ai answers /responses and /messages but
// returns 404 for /chat/completions.
var protocols = []string{"openai", "openai-responses", "anthropic", "gemini"}

func ValidProvider(provider string) bool {
	switch provider {
	case "openai", "anthropic", "gemini", "fireworks", "openrouter":
		return true
	}
	return customProvider.MatchString(provider)
}

// NormalizeProvider validates settings shared by the dashboard and REST API.
// BaseURL is the complete API prefix, including /v1 where required. For the
// "anthropic" protocol it is the full messages URL, which is the address that
// client sends to verbatim.
//
// The returned protocol is empty for a provider-native key and is otherwise
// always one of protocols, so callers never have to re-derive the default.
func NormalizeProvider(provider, baseURL, models, key, protocol string) (string, string, string, error) {
	if !ValidProvider(provider) {
		return "", "", "", fmt.Errorf("invalid provider")
	}
	if strings.ContainsAny(key, "\r\n\x00") {
		return "", "", "", fmt.Errorf("key must be a single line")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if provider == "openrouter" && baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(baseURL, "\r\n\x00") {
			return "", "", "", fmt.Errorf("base URL must be an HTTP(S) API prefix without credentials, query or fragment")
		}
	}
	if strings.HasPrefix(provider, "custom-") && baseURL == "" {
		return "", "", "", fmt.Errorf("custom provider requires a base URL")
	}
	if baseURL != "" && (provider == "anthropic" || provider == "gemini") {
		return "", "", "", fmt.Errorf("custom endpoints require a custom or OpenAI-compatible provider")
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch {
	case baseURL == "" && protocol != "":
		return "", "", "", fmt.Errorf("protocol requires a base URL")
	case baseURL != "" && protocol == "":
		protocol = DefaultProtocol
	case protocol != "" && !slices.Contains(protocols, protocol):
		return "", "", "", fmt.Errorf("protocol must be one of %s", strings.Join(protocols, ", "))
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
			return "", "", "", fmt.Errorf("model IDs must not contain whitespace")
		}
		if !seen[name] {
			normalized = append(normalized, name)
			seen[name] = true
		}
	}
	if baseURL != "" && len(normalized) == 0 {
		return "", "", "", fmt.Errorf("specify at least one model ID for this endpoint")
	}
	if baseURL == "" && len(normalized) > 0 {
		return "", "", "", fmt.Errorf("model IDs require a base URL")
	}
	if key == "" && !strings.HasPrefix(provider, "custom-") {
		return "", "", "", fmt.Errorf("key is required")
	}
	return baseURL, strings.Join(normalized, ","), protocol, nil
}

// SaveProviderKey replaces an owner's provider settings atomically.
//
// An empty key on a provider that already has one keeps the stored credential.
// Saving replaces the whole row, and the key is the one field the dashboard
// cannot show back — so anyone editing an endpoint's models or protocol would
// otherwise wipe the credential by leaving a field they were never shown blank,
// and the agent would start answering 401 with nothing on screen to explain it.
// Clearing a key means deleting the connection.
func (db *DB) SaveProviderKey(id, owner, provider, key, baseURL, models, protocol string, encKey []byte) error {
	baseURL, models, protocol, err := NormalizeProvider(provider, baseURL, models, key, protocol)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encrypted []byte
	if key == "" {
		// Read before the delete below removes the row it lives on. A miss is
		// simply a connection that never had a key.
		err = tx.QueryRow(`SELECT encrypted_key FROM api_keys WHERE owner_id = ? AND lower(provider) = ?`, owner, provider).Scan(&encrypted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("carry over existing key: %w", err)
		}
	}
	if encrypted == nil {
		if encrypted, err = encryptAESGCM(encKey, []byte(key)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM api_keys WHERE owner_id = ? AND lower(provider) = ?`, owner, provider); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO api_keys (id, owner_id, provider, encrypted_key, base_url, models, protocol) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, owner, provider, encrypted, baseURL, models, protocol); err != nil {
		return err
	}
	if err = pruneDefaultModel(tx, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// UserModelPrefix marks the agent-side models seeded from an owner's own
// connections, which is what tells them apart from the deployment-wide ones.
const UserModelPrefix = "svkexe_user:"

// UserModelID is the agent-side identifier of a model reached through one of
// the owner's own connections. The gateway stores the owner's chosen default as
// this ID, so building it lives here rather than beside the code that seeds it:
// the stored choice and the seeded row have to agree or the VM opens on a model
// that is not in its database.
func UserModelID(provider, model string) string {
	return UserModelPrefix + provider + ":" + model
}

// ownerModelIDs returns the agent-side IDs of every model the owner can reach
// through their own connections, in the order they are offered.
func ownerModelIDs(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, owner string) ([]string, error) {
	rows, err := q.Query(`SELECT provider, models FROM api_keys WHERE owner_id = ? AND base_url != '' ORDER BY created_at DESC, id`, owner)
	if err != nil {
		return nil, fmt.Errorf("list owner models: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var provider, models string
		if err := rows.Scan(&provider, &models); err != nil {
			return nil, fmt.Errorf("scan owner models: %w", err)
		}
		for _, model := range strings.Split(models, ",") {
			if model != "" {
				ids = append(ids, UserModelID(provider, model))
			}
		}
	}
	return ids, rows.Err()
}

// OwnerModelIDs returns the models the owner may choose a default from.
func (db *DB) OwnerModelIDs(owner string) ([]string, error) {
	return ownerModelIDs(db, owner)
}

// pruneDefaultModel drops a chosen default the owner can no longer reach.
// Editing or deleting a connection can retire the very model their VMs open on,
// and a dangling choice is worse than none: it is preferred over the models they
// do have, so every VM would keep opening on a model its database lacks.
func pruneDefaultModel(tx *sql.Tx, owner string) error {
	ids, err := ownerModelIDs(tx, owner)
	if err != nil {
		return err
	}
	var current string
	err = tx.QueryRow(`SELECT default_model FROM users WHERE id = ?`, owner).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read default model: %w", err)
	}
	if current == "" || slices.Contains(ids, current) {
		return nil
	}
	if _, err := tx.Exec(`UPDATE users SET default_model = '' WHERE id = ?`, owner); err != nil {
		return fmt.Errorf("clear default model: %w", err)
	}
	return nil
}

// ErrUnknownModel reports a chosen model the owner cannot reach. It is a
// caller's mistake rather than a failure, so handlers answer it with a 400
// while any other error stays an internal one.
var ErrUnknownModel = errors.New("unknown model")

// SetUserDefaultModel records the model this owner's VMs open on. An empty
// choice means "whatever the gateway picks" and is always allowed; anything else
// must be a model they can actually reach, because the value is written into
// guest configuration where a typo simply leaves the agent unable to answer.
//
// The write happens before the check, and an unreachable model is rolled back.
// Validating first would put the two in separate transactions, and a connection
// deleted in between would then be validated against and stored anyway, leaving
// default_model naming a model with no connection behind it. Writing first also
// keeps the transaction from having to upgrade a read snapshot to a write lock,
// which SQLite refuses outright once anyone else has committed.
func (db *DB) SetUserDefaultModel(owner, model string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("set default model: begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET default_model = ? WHERE id = ?`, model, owner)
	if err != nil {
		return fmt.Errorf("set default model: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set default model: rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if model != "" {
		ids, err := ownerModelIDs(tx, owner)
		if err != nil {
			return err
		}
		if !slices.Contains(ids, model) {
			return fmt.Errorf("%w %q", ErrUnknownModel, model)
		}
	}
	return tx.Commit()
}

// UserDefaultModel returns the owner's chosen default, or the empty string when
// they have made no choice or no longer exist. Setup uses it to decide what a
// new VM opens on, and a VM must still come up for an account mid-deletion.
func (db *DB) UserDefaultModel(owner string) (string, error) {
	var model string
	err := db.QueryRow(`SELECT default_model FROM users WHERE id = ?`, owner).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read default model: %w", err)
	}
	return model, nil
}

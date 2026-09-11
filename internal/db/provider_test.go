package db

import (
	"database/sql"
	"errors"
	"testing"
)

func TestProviderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, provider, url, models, key, protocol string
		valid                                      bool
	}{
		{"router", "openrouter", "", "vendor/model", "key", "", true},
		{"local", "custom-local", "http://localhost:8000/v1", "model", "", "", true},
		{"legacy", "anthropic", "", "", "key", "", true},
		{"missing URL", "custom-local", "", "model", "key", "", false},
		{"missing models", "openrouter", "", "", "key", "", false},
		{"bad protocol", "custom-local", "file:///tmp/models", "model", "key", "", false},
		{"credentials", "custom-local", "https://user:pass@host/v1", "model", "key", "", false},
		{"query", "custom-local", "https://host/v1?token=secret", "model", "key", "", false},
		{"env injection", "openai", "", "", "key\nEVIL=value", "", false},
		{"provider injection", "custom-a=b", "https://host/v1", "model", "key", "", false},
		// api.openmodel.ai serves /responses and /messages but 404s on
		// /chat/completions, so naming the wire protocol has to be allowed.
		{"responses", "custom-openmodel", "https://api.openmodel.ai/v1", "deepseek-v4-flash", "key", "openai-responses", true},
		{"messages", "custom-openmodel", "https://api.openmodel.ai/v1/messages", "deepseek-v4-flash", "key", "anthropic", true},
		{"unknown protocol", "custom-local", "https://host/v1", "model", "key", "grpc", false},
		{"protocol without URL", "openai", "", "", "key", "openai", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := NormalizeProvider(tc.provider, tc.url, tc.models, tc.key, tc.protocol)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// An endpoint stored before the protocol was configurable was seeded as
// chat/completions, so an unset protocol has to keep meaning exactly that
// rather than becoming an error or a different wire format.
func TestProviderProtocolDefaultsToChatCompletions(t *testing.T) {
	_, _, protocol, err := NormalizeProvider("custom-local", "https://host/v1", "model", "key", "")
	if err != nil || protocol != DefaultProtocol {
		t.Fatalf("protocol=%q err=%v", protocol, err)
	}
	_, _, protocol, err = NormalizeProvider("openai", "", "", "key", "")
	if err != nil || protocol != "" {
		t.Fatalf("provider-native key got protocol %q, err=%v", protocol, err)
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
	for _, column := range []string{"base_url", "models", "protocol"} {
		if _, err := database.Exec("ALTER TABLE api_keys DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec("ALTER TABLE users DROP COLUMN default_model"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := database.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := database.ListAPIKeysByOwner("old")
	if err != nil || len(keys) != 1 || keys[0].BaseURL != "" || keys[0].Protocol != "" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}

	// An endpoint stored before the protocol column existed is backfilled, so
	// the empty value keeps exactly one meaning: no endpoint at all.
	if _, err := database.Exec(
		`INSERT INTO api_keys (id, owner_id, provider, encrypted_key, base_url, models) VALUES ('legacy-endpoint', 'old', 'custom-old', x'00', 'https://host/v1', 'm')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := database.migrate(); err != nil {
		t.Fatal(err)
	}
	after, err := database.ListAPIKeysByOwner("old")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range after {
		if (k.BaseURL == "") != (k.Protocol == "") {
			t.Fatalf("protocol and endpoint disagree: %+v", k)
		}
	}
	plain, err := database.GetAPIKeyPlaintext("old-key", testEncKey)
	if err != nil || plain != "secret" {
		t.Fatal("migration lost key")
	}
	user, err := database.GetUserByID("old")
	if err != nil || user.DefaultModel != "" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}

// The chosen default is written into guest configuration, so it must name a
// model the owner can actually reach — and must stop naming one the moment
// they stop being able to reach it.
func TestDefaultModelFollowsTheOwnersConnections(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("k1", "owner", "custom-openmodel", "key", "https://api.openmodel.ai/v1", "deepseek-v4-flash,deepseek-v4-pro", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	chosen := UserModelID("custom-openmodel", "deepseek-v4-pro")
	if err := database.SetUserDefaultModel("owner", chosen); err != nil {
		t.Fatal(err)
	}
	if got, err := database.UserDefaultModel("owner"); err != nil || got != chosen {
		t.Fatalf("default=%q err=%v", got, err)
	}
	if err := database.SetUserDefaultModel("owner", UserModelID("custom-openmodel", "not-configured")); err == nil {
		t.Fatal("accepted a model the owner cannot reach")
	}

	// Re-saving the connection without that model retires it, so the choice
	// must not survive: it would be preferred over the models they do have.
	if err := database.SaveProviderKey("k2", "owner", "custom-openmodel", "key", "https://api.openmodel.ai/v1", "deepseek-v4-flash", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	if got, err := database.UserDefaultModel("owner"); err != nil || got != "" {
		t.Fatalf("stale default survived an edit: %q err=%v", got, err)
	}

	// Deleting the last connection does the same.
	if err := database.SetUserDefaultModel("owner", UserModelID("custom-openmodel", "deepseek-v4-flash")); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteAPIKeyForOwner("k2", "owner"); err != nil {
		t.Fatal(err)
	}
	if got, err := database.UserDefaultModel("owner"); err != nil || got != "" {
		t.Fatalf("stale default survived a delete: %q err=%v", got, err)
	}
}

// A rejected choice must leave nothing behind. The write happens before the
// check, so a validation failure that did not roll back would store exactly the
// unreachable model the check exists to keep out.
func TestRejectedDefaultModelIsRolledBack(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("k1", "owner", "custom-openmodel", "key", "https://api.openmodel.ai/v1", "deepseek-v4-flash", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	reachable := UserModelID("custom-openmodel", "deepseek-v4-flash")
	if err := database.SetUserDefaultModel("owner", reachable); err != nil {
		t.Fatal(err)
	}

	err := database.SetUserDefaultModel("owner", UserModelID("custom-openmodel", "not-mine"))
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err=%v, want ErrUnknownModel so handlers can answer 400", err)
	}
	if got, err := database.UserDefaultModel("owner"); err != nil || got != reachable {
		t.Fatalf("default=%q err=%v, want the previous choice intact", got, err)
	}

	// An account that no longer exists is reported as such, not as a bad model.
	if err := database.SetUserDefaultModel("ghost", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v, want sql.ErrNoRows", err)
	}
}

// Saving replaces the whole row, and the stored key is the one field the
// dashboard can never show back. So a blank key on a connection that has one
// keeps it — otherwise editing an endpoint's models or protocol would wipe the
// credential by leaving a field the owner was never shown empty, and the agent
// would answer 401 with nothing on screen to explain it.
func TestSavingWithoutAKeyKeepsTheStoredOne(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("k1", "owner", "custom-openmodel", "om-secret", "https://api.openmodel.ai/v1", "deepseek-v4-flash", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	// Re-saved with a changed model list and no key, exactly as the Edit button
	// submits it.
	if err := database.SaveProviderKey("k2", "owner", "custom-openmodel", "", "https://api.openmodel.ai/v1", "deepseek-v4-pro", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	keys, err := database.ListAPIKeysByOwner("owner")
	if err != nil || len(keys) != 1 || keys[0].Models != "deepseek-v4-pro" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	plain, err := database.GetAPIKeyPlaintext(keys[0].ID, testEncKey)
	if err != nil || plain != "om-secret" {
		t.Fatalf("key=%q err=%v, want the stored credential carried over", plain, err)
	}

	// A connection that never had one still ends up with none.
	if err := database.SaveProviderKey("k3", "owner", "custom-local", "", "http://localhost:8000/v1", "local/model", "", testEncKey); err != nil {
		t.Fatal(err)
	}
	local, err := database.ListAPIKeysByOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range local {
		if k.Provider != "custom-local" {
			continue
		}
		if plain, err := database.GetAPIKeyPlaintext(k.ID, testEncKey); err != nil || plain != "" {
			t.Fatalf("key=%q err=%v, want empty", plain, err)
		}
	}
}

// Deleting a connection prunes a default model, so the delete has to be scoped
// to its owner in the same breath: an unscoped prune would let one account's
// request reset another account's VMs.
func TestDeleteConnectionIsScopedToItsOwner(t *testing.T) {
	database := openTestDB(t)
	for _, id := range []string{"owner", "stranger"} {
		if _, err := database.EnsureUser(id, id+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.SaveProviderKey("k1", "owner", "custom-openmodel", "key", "https://api.openmodel.ai/v1", "deepseek-v4-flash", "openai-responses", testEncKey); err != nil {
		t.Fatal(err)
	}
	chosen := UserModelID("custom-openmodel", "deepseek-v4-flash")
	if err := database.SetUserDefaultModel("owner", chosen); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteAPIKeyForOwner("k1", "stranger"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stranger deleted another account's connection: %v", err)
	}
	keys, err := database.ListAPIKeysByOwner("owner")
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	if got, err := database.UserDefaultModel("owner"); err != nil || got != chosen {
		t.Fatalf("default=%q err=%v, want it untouched", got, err)
	}

	// The stranger cannot pick it either.
	if err := database.SetUserDefaultModel("stranger", chosen); err == nil {
		t.Fatal("stranger adopted another account's model")
	}
	if got, err := database.OwnerModelIDs("stranger"); err != nil || len(got) != 0 {
		t.Fatalf("models=%v err=%v, want none", got, err)
	}
}

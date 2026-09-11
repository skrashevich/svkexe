package picoclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/secrets"
)

const testEncKey = "01234567890123456789012345678901"

// ownerWithModels gives the owner one connection carrying the named models and
// returns the gateway database and a materializer over it.
func ownerWithModels(t *testing.T, models string) (*db.DB, *secrets.Materializer) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("key", "owner", "custom-openmodel", "secret", "https://api.openmodel.ai/v1", models, "openai-responses", []byte(testEncKey)); err != nil {
		t.Fatal(err)
	}
	return database, secrets.NewMaterializer(database, []byte(testEncKey), t.TempDir())
}

// A VM created after the owner picked a model must open on that model. The
// choice lives on the account and the VM did not exist when it was made, so
// setup reading it from the database is the only thing that carries it over.
func TestSetupOpensNewVMOnTheOwnersChosenModel(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	database, m := ownerWithModels(t, "deepseek-v4-flash,deepseek-v4-pro")
	chosen := db.UserModelID("custom-openmodel", "deepseek-v4-pro")
	if err := database.SetUserDefaultModel("owner", chosen); err != nil {
		t.Fatal(err)
	}
	guest := &guestRuntime{files: map[string][]byte{}}
	cfg := &LLMProxyConfig{BaseURL: "http://gateway/api/llm/v1", Token: "token", Models: []string{"cohere/north-mini-code:free"}}

	if err := SetupContainer(t.Context(), guest, database, m, testContainer("owner"), cfg); err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	// Not deepseek-v4-flash: that is merely the first model of the connection,
	// which is what the VM would open on if the choice were ignored.
	if config["default_model"] != chosen {
		t.Fatalf("default model = %q, want the owner's chosen %q", config["default_model"], chosen)
	}
}

// A restart must not undo what the owner set inside the agent. Setup used to
// rebuild the config from an empty map, which both dropped every other setting
// the agent keeps there and reset the model on every start — the opposite of
// what the same rule does for a VM that stays up.
func TestSetupKeepsTheOwnersInAgentChoiceAndOtherSettings(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	database, m := ownerWithModels(t, "deepseek-v4-flash,deepseek-v4-pro")
	// The owner left the gateway choice on Auto and picked in the agent itself.
	if got, err := database.UserDefaultModel("owner"); err != nil || got != "" {
		t.Fatalf("default=%q err=%v, want no stored choice", got, err)
	}
	inAgent := db.UserModelID("custom-openmodel", "deepseek-v4-pro")
	guest := &guestRuntime{
		files: map[string][]byte{ConfigFilePath: []byte(`{"default_model":"` + inAgent + `","theme":"dark"}`)},
	}
	cfg := &LLMProxyConfig{BaseURL: "http://gateway/api/llm/v1", Token: "token", Models: []string{"cohere/north-mini-code:free"}}

	if err := SetupContainer(t.Context(), guest, database, m, testContainer("owner"), cfg); err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	if config["default_model"] != inAgent {
		t.Fatalf("default model = %q, want the owner's in-agent choice %q", config["default_model"], inAgent)
	}
	if config["theme"] != "dark" {
		t.Fatalf("unrelated setting dropped: %v", config)
	}
}

// An explicit choice is the whole point of the setting: it has to reach a VM
// that is already running on a different model of the owner's, which the
// gateway would otherwise leave alone as their in-agent preference.
func TestRefreshAppliesTheOwnersChosenModelOverAnotherOfTheirs(t *testing.T) {
	database, m := ownerWithModels(t, "deepseek-v4-flash,deepseek-v4-pro")
	chosen := db.UserModelID("custom-openmodel", "deepseek-v4-pro")
	if err := database.SetUserDefaultModel("owner", chosen); err != nil {
		t.Fatal(err)
	}
	guest := &guestRuntime{files: map[string][]byte{
		ConfigFilePath: []byte(`{"default_model":"` + db.UserModelID("custom-openmodel", "deepseek-v4-flash") + `"}`),
	}}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", chosen, nil); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != chosen {
		t.Fatalf("default model = %q, want the owner's chosen %q", got, chosen)
	}
}

// Removing the last connection deletes the owner's models from the agent
// database. A guest left naming one would open on a model it does not have, so
// the reference has to go — replaced by a platform model the VM still holds, and
// otherwise removed. Refresh does not seed the platform list, so it must not
// name a model from it on faith.
func TestRefreshClearsDefaultLeftByARemovedConnection(t *testing.T) {
	for _, tc := range []struct {
		name, inTheVM, want string
	}{
		{"moves to a platform model the VM still has", "svkexe-cohere/north-mini-code:free\n", "svkexe-cohere/north-mini-code:free"},
		{"and clears it when the VM has nothing else", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, m := ownerWithModels(t, "deepseek-v4-flash")
			stale := db.UserModelID("custom-openmodel", "deepseek-v4-flash")
			if err := database.DeleteAPIKey("key"); err != nil {
				t.Fatal(err)
			}
			guest := &guestRuntime{
				files:  map[string][]byte{ConfigFilePath: []byte(`{"default_model":"` + stale + `"}`)},
				models: tc.inTheVM,
			}

			if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
				t.Fatal(err)
			}
			if got := guestDefaultModel(t, guest); got != tc.want {
				t.Fatalf("default model = %q, want %q", got, tc.want)
			}
		})
	}
}

// A model the gateway did not seed is the user's own creation inside the agent.
// Clearing a stale reference must not disturb it.
func TestRefreshLeavesAModelTheGatewayDoesNotOwn(t *testing.T) {
	database, m := ownerWithModels(t, "deepseek-v4-flash")
	if err := database.DeleteAPIKey("key"); err != nil {
		t.Fatal(err)
	}
	guest := &guestRuntime{
		files:  map[string][]byte{ConfigFilePath: []byte(`{"default_model":"hand-made-local"}`)},
		models: "hand-made-local\n",
	}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != "hand-made-local" {
		t.Fatalf("default model = %q, want the agent-side model untouched", got)
	}
}

// The agent keeps numbers, booleans and objects in its config alongside the one
// string the gateway owns. Decoding those into strings does not fail loudly —
// encoding/json stores the empty string — so a careless round-trip rewrites
// "max_tokens": 8192 as "max_tokens": "" and the agent can no longer parse its
// own configuration.
func TestRefreshPreservesNonStringConfigSettings(t *testing.T) {
	_, m := ownerWithModels(t, "deepseek-v4-flash")
	// A gateway-seeded model from a connection that is gone, so the config is
	// genuinely rewritten and the neighbouring settings go through the round
	// trip rather than being left untouched by an early return.
	guest := &guestRuntime{files: map[string][]byte{
		ConfigFilePath: []byte(`{"default_model":"svkexe_user:removed:model","max_tokens":8192,"debug":true,"tools":{"a":1}}`),
	}}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(guest.files[ConfigFilePath], &cfg); err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg["max_tokens"].(float64); !ok || got != 8192 {
		t.Fatalf("max_tokens = %#v, want the number 8192", cfg["max_tokens"])
	}
	if got, ok := cfg["debug"].(bool); !ok || !got {
		t.Fatalf("debug = %#v, want true", cfg["debug"])
	}
	if _, ok := cfg["tools"].(map[string]any); !ok {
		t.Fatalf("tools = %#v, want an object", cfg["tools"])
	}
	if cfg["default_model"] != db.UserModelID("custom-openmodel", "deepseek-v4-flash") {
		t.Fatalf("default_model = %#v", cfg["default_model"])
	}
}

// A gateway model the owner picked in the agent UI is still their choice. Adding
// a provider-native key — one with no endpoint, and so no models of their own —
// must not move them off it.
func TestRefreshLeavesAStillReachableGatewayModel(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	// A provider-native key: it becomes an env credential, not a model row.
	if err := database.SaveProviderKey("key", "owner", "anthropic", "sk-ant-x", "", "", "", []byte(testEncKey)); err != nil {
		t.Fatal(err)
	}
	m := secrets.NewMaterializer(database, []byte(testEncKey), t.TempDir())
	guest := &guestRuntime{
		files:  map[string][]byte{ConfigFilePath: []byte(`{"default_model":"svkexe-second/model"}`)},
		models: "svkexe-first/model\nsvkexe-second/model\n",
	}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != "svkexe-second/model" {
		t.Fatalf("default model = %q, want the owner's gateway choice preserved", got)
	}
}

// A model the owner defined inside the agent — an Ollama entry, say — is not
// the gateway's to move them off. Adding a connection in the dashboard gives
// them models of their own, but that is not a reason to overrule a choice the
// gateway never made.
func TestRefreshLeavesTheOwnersOwnAgentModelEvenWithConnections(t *testing.T) {
	_, m := ownerWithModels(t, "deepseek-v4-flash")
	guest := &guestRuntime{
		files:  map[string][]byte{ConfigFilePath: []byte(`{"default_model":"ollama-local"}`)},
		models: "ollama-local\nsvkexe-platform/model\n",
	}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := guestDefaultModel(t, guest); got != "ollama-local" {
		t.Fatalf("default model = %q, want the owner's own agent model untouched", got)
	}
}

// The operator lists their models in preference order — the LLM proxy tries them
// in it. A VM's list comes back alphabetical, so picking the first of that would
// quietly put the VM's default out of step with the proxy's primary.
func TestSetupFollowsTheOperatorsPlatformOrder(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	guest := &guestRuntime{files: map[string][]byte{}}
	// Deliberately not alphabetical: z-ai sorts last but is listed first.
	cfg := &LLMProxyConfig{
		BaseURL: "http://gateway/api/llm/v1", Token: "token",
		Models: []string{"z-ai/glm-4.6", "openai/gpt-oss-120b:free"},
	}

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), cfg); err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	if config["default_model"] != "svkexe-z-ai/glm-4.6" {
		t.Fatalf("default model = %q, want the operator's first model", config["default_model"])
	}
}

// A default_model set to "", to null, or to a number all decode to the same
// empty string, so a refresh that decided by decoded value alone would leave
// any of them sitting in the file — a value the agent cannot use, in exactly
// the case the clearing exists for.
func TestRefreshClearsAnUnusableDefaultModel(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"empty string", `{"default_model":"","other":1}`},
		{"null", `{"default_model":null,"other":1}`},
		{"a number", `{"default_model":42,"other":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, m := ownerWithModels(t, "deepseek-v4-flash")
			if err := database.DeleteAPIKey("key"); err != nil {
				t.Fatal(err)
			}
			// A deployment with no platform models either, so nothing is wanted.
			guest := &guestRuntime{
				files:  map[string][]byte{ConfigFilePath: []byte(tc.config)},
				models: " \n",
			}

			if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := json.Unmarshal(guest.files[ConfigFilePath], &cfg); err != nil {
				t.Fatal(err)
			}
			if _, present := cfg["default_model"]; present {
				t.Fatalf("unusable default left in place: %s", guest.files[ConfigFilePath])
			}
			if cfg["other"] != float64(1) {
				t.Fatalf("neighbouring setting lost: %s", guest.files[ConfigFilePath])
			}
		})
	}
}

// A config the gateway cannot read is not a config it may replace. A missing
// file is empty output and legitimately empty; an exec failure says nothing
// about the file, and rewriting on an empty map would wipe every setting the
// agent keeps there.
func TestRefreshDoesNotReplaceAConfigItCouldNotRead(t *testing.T) {
	_, m := ownerWithModels(t, "deepseek-v4-flash")
	original := []byte(`{"default_model":"ollama-local","theme":"dark","max_tokens":8192}`)
	guest := &guestRuntime{
		files:  map[string][]byte{ConfigFilePath: original},
		models: "ollama-local\n",
		fail:   "cat " + ConfigFilePath,
	}

	if err := RefreshProviderKeys(t.Context(), guest, m, "id", "vm", "owner", "", nil); err == nil {
		t.Fatal("an unreadable config was reported as a successful refresh")
	}
	if got := string(guest.files[ConfigFilePath]); got != string(original) {
		t.Fatalf("config rewritten from an unreadable read: %s", got)
	}
}

// The seeded row carries the wire protocol, not a fixed 'openai'. Getting this
// wrong is silent at seed time and fatal at use: api.openmodel.ai answers 404
// for /chat/completions, which is the path the 'openai' type posts to.
func TestProviderModelsSQLCarriesTheEndpointProtocol(t *testing.T) {
	sql := providerModelsSQL([]secrets.ProviderModel{{
		Provider: "custom-openmodel", Model: "deepseek-v4-flash",
		BaseURL: "https://api.openmodel.ai/v1", Key: "secret", Protocol: "openai-responses",
	}})
	// The whole tuple, in column order: asserting only that the protocol string
	// appears somewhere would pass if it landed in display_name or endpoint,
	// which is the mistake the column order exists to prevent.
	want := "VALUES ('svkexe_user:custom-openmodel:deepseek-v4-flash', " +
		"'custom-openmodel / deepseek-v4-flash', 'openai-responses', " +
		"'https://api.openmodel.ai/v1', 'secret', 'deepseek-v4-flash', 200000)"
	if !strings.Contains(sql, want) {
		t.Fatalf("seeded row is not\n%s\ngot\n%s", want, sql)
	}
}

// desiredModel is the one rule every path uses, so its table is where the
// priorities are pinned: an explicit choice, then the owner's own models, then
// whatever the VM already runs on — and never a model the VM does not have.
func TestDesiredModel(t *testing.T) {
	const (
		flash   = "svkexe_user:custom-openmodel:deepseek-v4-flash"
		pro     = "svkexe_user:custom-openmodel:deepseek-v4-pro"
		gateway = "svkexe-cohere/north-mini-code:free"
		other   = "svkexe-vendor/other"
		local   = "hand-made-local"
	)
	own := []string{flash, pro}
	for _, tc := range []struct {
		name            string
		available, own  []string
		chosen, current string
		want            string
	}{
		{"an explicit choice wins", []string{flash, pro, gateway}, own, pro, flash, pro},
		{"a retired choice does not", []string{flash, gateway}, own, pro, gateway, flash},
		{"own models replace the platform's", []string{flash, pro, gateway}, own, "", gateway, flash},
		{"among their own, the VM's own pick stands", []string{flash, pro, gateway}, own, "", pro, pro},
		{"with none of their own, what works stays", []string{gateway, other}, nil, "", other, other},
		{"including a model the gateway never seeded", []string{local, gateway}, nil, "", local, local},
		{"a model the VM lacks is replaced", []string{gateway}, nil, "", flash, gateway},
		{"and an empty VM names nothing", nil, nil, pro, flash, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := desiredModel(tc.available, tc.own, nil, tc.chosen, tc.current); got != tc.want {
				t.Fatalf("desired model = %q, want %q", got, tc.want)
			}
		})
	}
}

package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

func providerModelsSQL(models []secrets.ProviderModel) string {
	var sql strings.Builder
	fmt.Fprintf(&sql, "PRAGMA busy_timeout=10000;\nBEGIN IMMEDIATE;\nDELETE FROM models WHERE model_id GLOB '%s*';\n", db.UserModelPrefix)
	for _, m := range models {
		fmt.Fprintf(&sql, "INSERT INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES (%s, %s, %s, %s, %s, %s, 200000);\n",
			sqlQuote(m.ID()), sqlQuote(m.Provider+" / "+m.Model), sqlQuote(m.Protocol), sqlQuote(m.BaseURL), sqlQuote(m.Key), sqlQuote(m.Model))
	}
	sql.WriteString("COMMIT;\n")
	return sql.String()
}

func seedProviderModels(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel) error {
	sqlPath := ConfigDir + "/provider-models.sql"
	if err := writeGuestFile(ctx, rt, name, sqlPath, []byte(providerModelsSQL(models))); err != nil {
		return err
	}
	apply := fmt.Sprintf("sqlite3 -bail %s < %s; result=$?; rm -f %s; exit $result", DBPath, sqlPath, sqlPath)
	_, err := rt.Exec(ctx, name, []string{"sh", "-c", apply})
	return err
}

// providerModelIDs are the agent-side IDs of the owner's own models, in the
// order their settings list them.
func providerModelIDs(models []secrets.ProviderModel) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID())
	}
	return ids
}

// ownModels are the owner's own models among the ones a VM holds. It recovers
// the set from the VM itself, for callers that know what the VM has but not
// what the owner's settings list.
func ownModels(available []string) []string {
	var own []string
	for _, id := range available {
		if strings.HasPrefix(id, db.UserModelPrefix) {
			own = append(own, id)
		}
	}
	return own
}

// desiredModel picks the model a VM should open on, choosing only from the
// models it actually has.
//
//	available — every model ID in the VM's agent database
//	own       — the owner's own model IDs, in the order their settings list them
//	chosen    — the model the owner picked in their LLM settings, empty for Auto
//	current   — the model the VM names today
//
// The owner's own models replace the platform's: that list is deployment-wide
// and can name models this account cannot reach, while a connection the owner
// configured is one they chose and pay for. Among their own, whatever the VM
// already names is their in-agent preference and stands — but a platform model
// does not get that protection, or adding a first connection would leave every
// running VM on the platform's key.
func desiredModel(available, own []string, chosen, current string) string {
	reachable := func(id string) bool { return id != "" && slices.Contains(available, id) }
	if reachable(chosen) {
		return chosen
	}
	var mine []string
	for _, id := range own {
		if reachable(id) {
			mine = append(mine, id)
		}
	}
	if len(mine) > 0 {
		if slices.Contains(mine, current) {
			return current
		}
		return mine[0]
	}
	// Nothing of their own to move them to. Whatever the VM runs on keeps
	// working, including a model the gateway never seeded — that one is the
	// owner's own creation inside the agent.
	if reachable(current) {
		return current
	}
	if gateway := firstWithPrefix(available, gatewayModelPrefix); gateway != "" {
		return gateway
	}
	if len(available) > 0 {
		return available[0]
	}
	return ""
}

// readGuestConfig returns the agent configuration as it stands in the VM.
//
// Values are kept as raw JSON rather than decoded. The gateway only ever reads
// and writes one string setting, but the agent keeps numbers, booleans and
// objects here too, and decoding those into strings does not fail cleanly —
// encoding/json records the type error and stores the empty string, so a
// round-trip would silently rewrite `"max_tokens": 8192` as `"max_tokens": ""`
// and leave the agent unable to parse its own configuration.
//
// A missing or unparseable file reads as empty. The config is rewritten from
// here either way, and refusing to proceed would leave a VM unable to start over
// a file the gateway is about to replace.
func readGuestConfig(ctx context.Context, rt runtime.ContainerRuntime, name string) map[string]json.RawMessage {
	cfg := map[string]json.RawMessage{}
	if out, err := rt.Exec(ctx, name, []string{"cat", ConfigFilePath}); err == nil {
		if err := json.Unmarshal(out, &cfg); err != nil {
			return map[string]json.RawMessage{}
		}
	}
	return cfg
}

// guestDefaultModelOf returns the default_model cfg names, or "" when it names
// none or names something that is not a string.
func guestDefaultModelOf(cfg map[string]json.RawMessage) string {
	var current string
	if err := json.Unmarshal(cfg["default_model"], &current); err != nil {
		return ""
	}
	return current
}

// writeGuestConfig stores cfg with default_model set to value, or removed when
// value is empty. Every other setting is carried through untouched.
func writeGuestConfig(ctx context.Context, rt runtime.ContainerRuntime, name string, cfg map[string]json.RawMessage, value string) error {
	if value == "" {
		delete(cfg, "default_model")
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		cfg["default_model"] = encoded
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeGuestFile(ctx, rt, name, ConfigFilePath, data)
}

// applyDefaultModel points a VM at the model its owner should be on, leaving the
// file untouched when it already names it. It must run after the models have
// been seeded, since it chooses from what the VM actually has.
//
// It reports whether the file changed, so a caller that restarts the agent can
// decline to do so for a no-op — a restart kills whatever the agent is in the
// middle of.
func applyDefaultModel(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel, chosen string) (bool, error) {
	available, err := listGuestModels(ctx, rt, name)
	if err != nil {
		return false, err
	}
	cfg := readGuestConfig(ctx, rt, name)
	current := guestDefaultModelOf(cfg)
	want := desiredModel(available, providerModelIDs(models), chosen, current)
	if want == current {
		return false, nil
	}
	return true, writeGuestConfig(ctx, rt, name, cfg, want)
}

// RefreshProviderKeys applies provider changes to the running agent, including
// deletion, key rotation and a change of default model. Restart reloads both env
// credentials and DB models.
//
// chosen is the owner's selected default model, empty when they have not picked
// one.
func RefreshProviderKeys(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, id, name, owner, chosen string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, _ := setupLocks.LoadOrStore(name, make(chan struct{}, 1))
	gate := lock.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := m.RefreshKeys(id, owner); err != nil {
		return err
	}
	env, err := m.ReadKeys(id)
	if err != nil {
		return err
	}
	if err := writeGuestFile(ctx, rt, name, EnvFilePath, env); err != nil {
		return err
	}
	models, err := m.ProviderModels(owner)
	if err != nil {
		return err
	}
	if err := seedProviderModels(ctx, rt, name, models); err != nil {
		return err
	}
	if _, err := applyDefaultModel(ctx, rt, name, models, chosen); err != nil {
		return err
	}
	protect := fmt.Sprintf("chown root:user %[1]s %[2]s && chmod 640 %[1]s %[2]s && systemctl restart picoclaw.service", EnvFilePath, ConfigFilePath)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", protect}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}

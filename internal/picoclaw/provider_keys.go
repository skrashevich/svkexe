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

// gatewaySeeded reports whether the gateway is the one that put this model in
// the VM. Anything else the guest names is the owner's own creation inside the
// agent, which the gateway has no business moving them off.
func gatewaySeeded(id string) bool {
	return strings.HasPrefix(id, db.UserModelPrefix) || strings.HasPrefix(id, gatewayModelPrefix)
}

// stillReachable reports whether the VM can still open on the model it names.
func stillReachable(current string, models []secrets.ProviderModel, llmCfg *LLMProxyConfig) bool {
	if current == "" {
		return false
	}
	if !gatewaySeeded(current) {
		return true
	}
	if slices.ContainsFunc(models, func(m secrets.ProviderModel) bool { return m.ID() == current }) {
		return true
	}
	if llmCfg == nil {
		return false
	}
	return slices.ContainsFunc(llmCfg.Models, func(m string) bool { return gatewayModelPrefix+m == current })
}

// resolveGuestDefaultModel decides what a guest's default_model should become,
// given what it currently names. It returns the new value — empty meaning the
// setting is removed — and whether anything has to change.
//
// An explicit choice is authoritative: the owner made it in their LLM settings
// precisely so that every VM of theirs would honour it, so a different model
// left in the guest is replaced.
//
// Without one the gateway is only guessing, and whatever the guest already
// names is a better guess than any list position: it is the owner's own in-agent
// preference. So it stands as long as it still resolves — and only when it does
// not is a replacement picked. That is the case that has to be acted on rather
// than skipped: removing the last connection deletes those models from the agent
// database, and a guest still naming one would open on a model it does not have.
func resolveGuestDefaultModel(current string, models []secrets.ProviderModel, chosen string, llmCfg *LLMProxyConfig) (value string, changed bool) {
	if chosen != "" && slices.ContainsFunc(models, func(m secrets.ProviderModel) bool { return m.ID() == chosen }) {
		return chosen, chosen != current
	}
	if stillReachable(current, models, llmCfg) {
		return current, false
	}
	// defaultModelID is asked for a fallback, not for the choice: a choice that
	// survived to here is one the owner can no longer reach.
	want := defaultModelID(models, "", llmCfg)
	return want, want != current
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

// applyDefaultModel points a running VM at the model its owner should be on,
// leaving the file untouched when it already names it.
func applyDefaultModel(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel, chosen string, llmCfg *LLMProxyConfig) error {
	cfg := readGuestConfig(ctx, rt, name)
	value, changed := resolveGuestDefaultModel(guestDefaultModelOf(cfg), models, chosen, llmCfg)
	if !changed {
		return nil
	}
	return writeGuestConfig(ctx, rt, name, cfg, value)
}

// RefreshProviderKeys applies provider changes to the running agent, including
// deletion, key rotation and a change of default model. Restart reloads both env
// credentials and DB models.
//
// chosen is the owner's selected default model, empty when they have not picked
// one. llmCfg may be nil, which simply leaves no gateway model to fall back to.
func RefreshProviderKeys(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, id, name, owner, chosen string, llmCfg *LLMProxyConfig) error {
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
	if err := applyDefaultModel(ctx, rt, name, models, chosen, llmCfg); err != nil {
		return err
	}
	protect := fmt.Sprintf("chown root:user %[1]s %[2]s && chmod 640 %[1]s %[2]s && systemctl restart picoclaw.service", EnvFilePath, ConfigFilePath)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", protect}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}

package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

// providerModelID is the agent-side identifier of a model backed by one of the
// owner's own keys.
func providerModelID(m secrets.ProviderModel) string {
	return userModelPrefix + m.Provider + ":" + m.Model
}

func providerModelsSQL(models []secrets.ProviderModel) string {
	var sql strings.Builder
	fmt.Fprintf(&sql, "PRAGMA busy_timeout=10000;\nBEGIN IMMEDIATE;\nDELETE FROM models WHERE model_id GLOB '%s*';\n", userModelPrefix)
	for _, m := range models {
		fmt.Fprintf(&sql, "INSERT INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES (%s, %s, 'openai', %s, %s, %s, 200000);\n",
			sqlQuote(providerModelID(m)), sqlQuote(m.Provider+" / "+m.Model), sqlQuote(m.BaseURL), sqlQuote(m.Key), sqlQuote(m.Model))
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

// adoptProviderDefaultModel points the VM's default model at one of the owner's
// own models once they have keys, unless the current default is a model of
// theirs that still exists. A gateway default is deployment-wide and may name a
// model this account cannot reach, so it must not survive the owner's own keys.
func adoptProviderDefaultModel(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel) error {
	if len(models) == 0 {
		return nil
	}
	cfg := map[string]string{}
	if out, err := rt.Exec(ctx, name, []string{"cat", ConfigFilePath}); err == nil {
		// An unreadable or corrupt config is rewritten rather than trusted.
		_ = json.Unmarshal(out, &cfg)
	}
	current := cfg["default_model"]
	if slices.ContainsFunc(models, func(m secrets.ProviderModel) bool { return providerModelID(m) == current }) {
		return nil
	}
	cfg["default_model"] = providerModelID(models[0])
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeGuestFile(ctx, rt, name, ConfigFilePath, data)
}

// RefreshProviderKeys applies provider changes to the running agent, including
// deletion and key rotation. Restart reloads both env credentials and DB models.
func RefreshProviderKeys(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, id, name, owner string) error {
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
	if err := adoptProviderDefaultModel(ctx, rt, name, models); err != nil {
		return err
	}
	protect := fmt.Sprintf("chown root:user %[1]s %[2]s && chmod 640 %[1]s %[2]s && systemctl restart picoclaw.service", EnvFilePath, ConfigFilePath)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", protect}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}

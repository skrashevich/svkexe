package picoclaw

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

func providerModelsSQL(models []secrets.ProviderModel) string {
	var sql strings.Builder
	sql.WriteString("PRAGMA busy_timeout=10000;\nBEGIN IMMEDIATE;\nDELETE FROM models WHERE model_id GLOB 'svkexe_user:*';\n")
	for _, m := range models {
		fmt.Fprintf(&sql, "INSERT INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES (%s, %s, 'openai', %s, %s, %s, 200000);\n",
			sqlQuote("svkexe_user:"+m.Provider+":"+m.Model), sqlQuote(m.Provider+" / "+m.Model), sqlQuote(m.BaseURL), sqlQuote(m.Key), sqlQuote(m.Model))
	}
	sql.WriteString("COMMIT;\n")
	return sql.String()
}

func seedProviderModels(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, name, owner string) error {
	models, err := m.ProviderModels(owner)
	if err != nil {
		return err
	}
	if err := writeGuestFile(ctx, rt, name, "/etc/shelley/provider-models.sql", []byte(providerModelsSQL(models))); err != nil {
		return err
	}
	_, err = rt.Exec(ctx, name, []string{"sh", "-c", "sqlite3 -bail /data/shelley.db < /etc/shelley/provider-models.sql; result=$?; rm -f /etc/shelley/provider-models.sql; exit $result"})
	return err
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
	if err := seedProviderModels(ctx, rt, m, name, owner); err != nil {
		return err
	}
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", "chown root:user /etc/shelley/env && chmod 640 /etc/shelley/env && systemctl restart picoclaw.service"}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}

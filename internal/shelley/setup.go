package shelley

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

// SetupContainer prepares a container for running Shelley by:
//  1. Creating the necessary directories inside the container.
//  2. Writing the systemd unit file.
//  3. Writing shelley.json config (llm_gateway + default_model).
//  4. Materializing LLM API keys.
//  5. Enabling and starting the service.
//
// containerID is the database UUID (used for host-side key materialization).
// incusName is the Incus container name (used for exec commands inside the container).
// llmCfg may be nil if no gateway-level LLM proxy is configured.
func SetupContainer(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, containerID, incusName, ownerID string, llmCfg *LLMProxyConfig) error {
	// Create required directories inside the container.
	for _, dir := range []string{"/data", "/etc/shelley", "/etc/systemd/system"} {
		if _, err := rt.Exec(ctx, incusName, []string{"mkdir", "-p", dir}); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Write systemd unit file.
	unitContent := SystemdUnitContent()
	writeCmd := []string{"sh", "-c", fmt.Sprintf("cat > /etc/systemd/system/shelley.service << 'SHELLEY_UNIT_EOF'\n%sSHELLEY_UNIT_EOF", unitContent)}
	if _, err := rt.Exec(ctx, incusName, writeCmd); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	// Write shelley.json config with llm_gateway and default_model.
	shelleyCfg := map[string]string{}
	if llmCfg != nil {
		if llmCfg.BaseURL != "" {
			shelleyCfg["llm_gateway"] = llmCfg.BaseURL
		}
		if len(llmCfg.Models) > 0 {
			shelleyCfg["default_model"] = llmCfg.Models[0]
		}
	}
	cfgJSON, err := json.Marshal(shelleyCfg)
	if err != nil {
		return fmt.Errorf("marshal shelley config: %w", err)
	}
	writeCfgCmd := []string{"sh", "-c", fmt.Sprintf("printf '%%s' %q > %s", string(cfgJSON), ConfigFilePath)}
	if _, err := rt.Exec(ctx, incusName, writeCfgCmd); err != nil {
		return fmt.Errorf("write shelley config: %w", err)
	}

	// Seed custom models into Shelley's SQLite database so they appear in the UI.
	// Each model from OPENROUTER_MODELS is inserted as a custom model pointing
	// to the gateway's LLM proxy endpoint. Uses INSERT OR REPLACE for idempotency.
	if llmCfg != nil && llmCfg.BaseURL != "" && len(llmCfg.Models) > 0 {
		var sqlStmts []string
		for _, model := range llmCfg.Models {
			// Derive display name from model ID: "anthropic/claude-sonnet-4" → "claude-sonnet-4"
			displayName := model
			if idx := strings.LastIndex(model, "/"); idx >= 0 {
				displayName = model[idx+1:]
			}
			// All models go through the gateway's OpenAI-compatible endpoint.
			stmt := fmt.Sprintf(
				"INSERT OR REPLACE INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES ('svkexe-%s', '%s', 'openai', '%s', '%s', '%s', 200000);",
				model, displayName, llmCfg.BaseURL, llmCfg.Token, model,
			)
			sqlStmts = append(sqlStmts, stmt)
		}
		seedSQL := strings.Join(sqlStmts, "\n")
		seedCmd := []string{"sh", "-c", fmt.Sprintf("sqlite3 %s %q", DBPath, seedSQL)}
		// Non-fatal: Shelley DB may not exist yet on first boot.
		rt.Exec(ctx, incusName, seedCmd)
	}

	// Materialize per-user API keys to the gateway host, then push into the container.
	if err := m.MaterializeKeys(containerID, ownerID); err != nil {
		return fmt.Errorf("materialize keys: %w", err)
	}
	envContent, err := m.ReadKeys(containerID)
	if err != nil {
		return fmt.Errorf("read materialized keys: %w", err)
	}
	if len(envContent) > 0 {
		writeEnvCmd := []string{"sh", "-c", fmt.Sprintf("cat > %s << 'ENV_EOF'\n%sENV_EOF", EnvFilePath, string(envContent))}
		if _, err := rt.Exec(ctx, incusName, writeEnvCmd); err != nil {
			return fmt.Errorf("write env to container: %w", err)
		}
	}

	// Enable and start the service.
	if _, err := rt.Exec(ctx, incusName, []string{"systemctl", "enable", "shelley.service"}); err != nil {
		return fmt.Errorf("enable shelley service: %w", err)
	}
	if _, err := rt.Exec(ctx, incusName, []string{"systemctl", "start", "shelley.service"}); err != nil {
		return fmt.Errorf("start shelley service: %w", err)
	}

	return nil
}

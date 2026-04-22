package shelley

import (
	"context"
	"encoding/json"
	"fmt"

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

	// Write shelley.json config so Shelley uses the gateway as its LLM backend.
	shelleyCfg := map[string]string{}
	if llmCfg != nil {
		if llmCfg.BaseURL != "" {
			shelleyCfg["llm_gateway"] = llmCfg.BaseURL
		}
		if llmCfg.DefaultModel != "" {
			shelleyCfg["default_model"] = llmCfg.DefaultModel
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

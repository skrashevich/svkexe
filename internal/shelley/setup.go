package shelley

import (
	"context"
	"fmt"

	"github.com/svkexe/platform/internal/runtime"
	"github.com/svkexe/platform/internal/secrets"
)

// SetupContainer prepares a container for running Shelley by:
//  1. Creating the necessary directories inside the container.
//  2. Writing the systemd unit file.
//  3. Writing LLM proxy env file (if configured).
//  4. Materializing LLM API keys.
//  5. Enabling and starting the service.
//
// llmCfg may be nil if no gateway-level LLM proxy is configured.
func SetupContainer(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, containerID, ownerID string, llmCfg *LLMProxyConfig) error {
	// Create required directories inside the container.
	for _, dir := range []string{"/data", "/etc/shelley", "/etc/systemd/system"} {
		if _, err := rt.Exec(ctx, containerID, []string{"mkdir", "-p", dir}); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Write systemd unit file.
	unitContent := SystemdUnitContent()
	writeCmd := []string{"sh", "-c", fmt.Sprintf("cat > /etc/systemd/system/shelley.service << 'SHELLEY_UNIT_EOF'\n%sSHELLEY_UNIT_EOF", unitContent)}
	if _, err := rt.Exec(ctx, containerID, writeCmd); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	// Write LLM proxy env file so Shelley uses the gateway as its LLM backend.
	if llmCfg != nil && llmCfg.BaseURL != "" {
		envContent := fmt.Sprintf("OPENAI_BASE_URL=%s\nOPENAI_API_KEY=%s\n", llmCfg.BaseURL, llmCfg.Token)
		writeEnvCmd := []string{"sh", "-c", fmt.Sprintf("cat > %s << 'LLM_ENV_EOF'\n%sLLM_ENV_EOF", LLMEnvFilePath, envContent)}
		if _, err := rt.Exec(ctx, containerID, writeEnvCmd); err != nil {
			return fmt.Errorf("write llm proxy env: %w", err)
		}
	}

	// Materialize per-user API keys.
	if err := m.MaterializeKeys(containerID, ownerID); err != nil {
		return fmt.Errorf("materialize keys: %w", err)
	}

	// Enable and start the service.
	if _, err := rt.Exec(ctx, containerID, []string{"systemctl", "enable", "shelley.service"}); err != nil {
		return fmt.Errorf("enable shelley service: %w", err)
	}
	if _, err := rt.Exec(ctx, containerID, []string{"systemctl", "start", "shelley.service"}); err != nil {
		return fmt.Errorf("start shelley service: %w", err)
	}

	return nil
}

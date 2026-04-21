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
//  3. Materializing LLM API keys.
func SetupContainer(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, containerID, ownerID string) error {
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

	// Enable the service.
	if _, err := rt.Exec(ctx, containerID, []string{"systemctl", "enable", "shelley.service"}); err != nil {
		return fmt.Errorf("enable shelley service: %w", err)
	}

	// Materialize API keys.
	if err := m.MaterializeKeys(containerID, ownerID); err != nil {
		return fmt.Errorf("materialize keys: %w", err)
	}

	return nil
}

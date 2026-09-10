package picoclaw

import (
	"context"
	"fmt"

	"github.com/skrashevich/svkexe/internal/runtime"
)

// BackupData stops the agent before archiving SQLite and refuses to let a
// recreate proceed when the backup cannot be read.
func BackupData(ctx context.Context, rt runtime.ContainerRuntime, name string) ([]byte, error) {
	fr, ok := rt.(runtime.FileRuntime)
	if !ok {
		return nil, fmt.Errorf("runtime cannot back up agent data")
	}
	cmd := `set -eu
for unit in picoclaw.service shelley.service; do
 if systemctl cat "$unit" >/dev/null 2>&1; then systemctl stop "$unit"; fi
done
tar -czf /tmp/data-backup.tar.gz -C / data`
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", cmd}); err != nil {
		return nil, fmt.Errorf("archive agent data: %w", err)
	}
	data, err := fr.PullFile(ctx, name, "/tmp/data-backup.tar.gz")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty agent backup")
	}
	return data, nil
}

// RestoreData must run before SetupContainer, while no agent has opened the DB.
func RestoreData(ctx context.Context, rt runtime.ContainerRuntime, name string, data []byte) error {
	fr, ok := rt.(runtime.FileRuntime)
	if !ok {
		return fmt.Errorf("runtime cannot restore agent data")
	}
	if err := fr.PushFile(ctx, name, "/tmp/data-backup.tar.gz", data); err != nil {
		return err
	}
	_, err := rt.Exec(ctx, name, []string{"sh", "-c", `set -eu
for unit in picoclaw.service shelley.service; do
 if systemctl cat "$unit" >/dev/null 2>&1; then systemctl stop "$unit"; fi
done
tar -xzf /tmp/data-backup.tar.gz -C /
rm -f /tmp/data-backup.tar.gz
chown -R user:user /data`})
	return err
}

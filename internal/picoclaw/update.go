package picoclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/runtime"
)

// AgentUpdate compares the running executable with the platform artifact.
// Hashes distinguish integration revisions with the same PicoClaw version.
type AgentUpdate struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
}

func CheckUpdate(ctx context.Context, rt runtime.ContainerRuntime, name string) (*AgentUpdate, error) {
	data, err := platformBinary()
	if err != nil {
		return nil, fmt.Errorf("read platform agent: %w", err)
	}
	out, err := rt.Exec(ctx, name, []string{"sh", "-c", `set -eu
pid=$(systemctl show --property MainPID --value picoclaw.service)
[ "$pid" -gt 0 ]
sha256sum /proc/$pid/exe`})
	if err != nil {
		return nil, fmt.Errorf("read running agent checksum: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return nil, fmt.Errorf("invalid running agent checksum")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return nil, fmt.Errorf("invalid running agent checksum: %w", err)
	}
	latest := fmt.Sprintf("%x", sha256.Sum256(data))
	return &AgentUpdate{CurrentVersion: fields[0], LatestVersion: latest, HasUpdate: fields[0] != latest}, nil
}

// Update installs the platform artifact and restarts only the agent service.
func Update(ctx context.Context, rt runtime.ContainerRuntime, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	lock, _ := setupLocks.LoadOrStore(name, make(chan struct{}, 1))
	gate := lock.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Require a host artifact; the image fallback is not an update.
	data, err := platformBinary()
	if err != nil {
		return fmt.Errorf("read platform agent: %w", err)
	}
	if err := installBinaryData(ctx, rt, name, data); err != nil {
		return err
	}
	if _, err := rt.Exec(ctx, name, []string{"systemctl", "restart", "picoclaw.service"}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}

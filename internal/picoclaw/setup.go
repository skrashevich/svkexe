package picoclaw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

var setupLocks sync.Map // container name -> turn-safe setup semaphore

// SetupContainer installs PicoClaw while retaining Shelley's UI, prompts and DB.
// It is used by every create/start/recreate path and is safe to repeat. The
// whole container is taken rather than its identifiers alone: the agent is also
// told the address and port its work will be served on, which only the record
// knows.
//
// database is passed through to the environment guide, which uses it to name
// the VM's custom domains. It may be nil in tests, which then simply get a
// guide without them.
func SetupContainer(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, m *secrets.Materializer, c *db.Container, llmCfg *LLMProxyConfig) error {
	if c == nil {
		return fmt.Errorf("setup PicoClaw: no container given")
	}
	containerID, incusName, ownerID := c.ID, c.IncusName, c.OwnerID
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	lock, _ := setupLocks.LoadOrStore(incusName, make(chan struct{}, 1))
	gate := lock.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := waitForGuestSystemd(ctx, rt, incusName); err != nil {
		return err
	}
	if _, err := rt.Exec(ctx, incusName, []string{"mkdir", "-p", "/data", ConfigDir, "/etc/systemd/system"}); err != nil {
		return err
	}
	if err := installBinary(ctx, rt, incusName); err != nil {
		return err
	}
	// Stop every historical unit name before touching configuration or the
	// database, preserve a one-time SQLite backup so rollback does not depend on
	// newer schema support, then move pre-rename state to the PicoClaw paths.
	// The moves are conditional so repeated setups stay idempotent.
	stop := fmt.Sprintf(`set -eu
for unit in shelley.service picoclaw.service; do
 if systemctl cat "$unit" >/dev/null 2>&1; then systemctl disable --now "$unit"; fi
done
if [ -f %[1]s ] && [ ! -f /data/picoclaw.pre-rename.db ]; then sqlite3 %[1]s '.backup /data/picoclaw.pre-rename.db'; fi
if [ -f %[1]s ] && [ ! -f %[2]s ]; then mv %[1]s %[2]s; fi
for suffix in -wal -shm; do
 if [ -f %[1]s$suffix ] && [ ! -f %[2]s$suffix ]; then mv %[1]s$suffix %[2]s$suffix; fi
done
if [ -f %[3]s/env ] && [ ! -f %[4]s ]; then mv %[3]s/env %[4]s; fi
rm -f /usr/local/bin/shelley /etc/systemd/system/shelley.service
rm -rf %[3]s
`, LegacyDBPath, DBPath, LegacyConfigDir, EnvFilePath)
	if _, err := rt.Exec(ctx, incusName, []string{"sh", "-c", stop}); err != nil {
		return fmt.Errorf("stop old agent: %w", err)
	}
	if err := writeGuestFile(ctx, rt, incusName, "/etc/systemd/system/picoclaw.service", []byte(SystemdUnitContent())); err != nil {
		return err
	}
	var providerModels []secrets.ProviderModel
	if m != nil {
		var err error
		if providerModels, err = m.ProviderModels(ownerID); err != nil {
			return fmt.Errorf("read provider models: %w", err)
		}
	}
	cfg := map[string]string{}
	// llm_gateway in the preserved frontend means exe.dev's provider-specific
	// API, not an OpenAI endpoint. Configure only explicit DB-backed models.
	if id := defaultModelID(providerModels, llmCfg); id != "" {
		cfg["default_model"] = id
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeGuestFile(ctx, rt, incusName, ConfigFilePath, data); err != nil {
		return err
	}
	// Written before the agent starts, so its first conversation already knows
	// where this VM's work is published.
	if err := writeEnvironmentGuide(ctx, rt, database, c); err != nil {
		return err
	}
	var env []byte
	if m != nil {
		if err := m.MaterializeKeys(containerID, ownerID); err != nil {
			return fmt.Errorf("materialize keys: %w", err)
		}
		env, err = m.ReadKeys(containerID)
		if err != nil {
			return err
		}
	}
	// Always replace the file: deleting the last key must clear stale credentials.
	if err := writeGuestFile(ctx, rt, incusName, EnvFilePath, env); err != nil {
		return err
	}
	configure := fmt.Sprintf(`set -eu
chown -R user:user /data
chown root:user %[1]s %[2]s
chmod 640 %[1]s %[2]s
systemctl daemon-reload
`, EnvFilePath, ConfigFilePath)
	if _, err := rt.Exec(ctx, incusName, []string{"sh", "-c", configure}); err != nil {
		return err
	}
	if err := startPicoClawService(ctx, rt, incusName); err != nil {
		return err
	}
	if err := SeedLLMModels(ctx, rt, incusName, llmCfg); err != nil {
		return err
	}
	if m != nil {
		if err := seedProviderModels(ctx, rt, incusName, providerModels); err != nil {
			return err
		}
	}
	// Custom models and the default model must be loaded with the current token.
	if _, err := rt.Exec(ctx, incusName, []string{"systemctl", "restart", "picoclaw.service"}); err != nil {
		return err
	}
	if err := waitForAgentHTTP(ctx, rt, incusName); err != nil {
		return err
	}
	log.Printf("picoclaw: setup complete for %s", incusName)
	return nil
}

// defaultModelID is the model a VM opens with. The owner's own keys win over
// the deployment-wide gateway list: that list is shared by every VM and can
// name models this account has no access to, while a key the owner configured
// is one they chose and can reach.
func defaultModelID(providerModels []secrets.ProviderModel, llmCfg *LLMProxyConfig) string {
	if len(providerModels) > 0 {
		return providerModelID(providerModels[0])
	}
	if llmCfg != nil && llmCfg.BaseURL != "" && len(llmCfg.Models) > 0 {
		return gatewayModelPrefix + llmCfg.Models[0]
	}
	return ""
}

func writeGuestFile(ctx context.Context, rt runtime.ContainerRuntime, name, path string, data []byte) error {
	// Only fixed paths and base64 enter the shell. Config/key bytes cannot become
	// command substitutions or terminate a here-document.
	cmd := fmt.Sprintf("umask 077; printf '%%s' '%s' | base64 -d > %s", base64.StdEncoding.EncodeToString(data), path)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", cmd}); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func installBinary(ctx context.Context, rt runtime.ContainerRuntime, name string) error {
	path := os.Getenv("SVKEXE_AGENT_BINARY")
	if path == "" {
		path = "/usr/local/lib/svkexe/picoclaw"
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv("SVKEXE_AGENT_BINARY") == "" {
		if exe, e := os.Executable(); e == nil {
			data, err = os.ReadFile(filepath.Join(filepath.Dir(exe), "picoclaw"))
		}
	}
	if os.IsNotExist(err) && os.Getenv("SVKEXE_AGENT_BINARY") == "" {
		// Fresh images already contain the pinned agent. Older images must receive
		// the host artifact; never silently fall back to the Shelley engine.
		if e := verifyBinary(ctx, rt, name, "/usr/local/bin/picoclaw"); e == nil {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("load PicoClaw binary (build make agent and set SVKEXE_AGENT_BINARY): %w", err)
	}
	fr, ok := rt.(runtime.FileRuntime)
	if !ok {
		return fmt.Errorf("runtime cannot install PicoClaw binary")
	}
	if err := fr.PushFile(ctx, name, "/usr/local/bin/picoclaw.new", data); err != nil {
		return err
	}
	if _, err := rt.Exec(ctx, name, []string{"chmod", "755", "/usr/local/bin/picoclaw.new"}); err != nil {
		return err
	}
	if err := verifyBinary(ctx, rt, name, "/usr/local/bin/picoclaw.new"); err != nil {
		return err
	}
	_, err = rt.Exec(ctx, name, []string{"mv", "-f", "/usr/local/bin/picoclaw.new", "/usr/local/bin/picoclaw"})
	return err
}

// Verify both guest architecture compatibility and the customized runtime
// before stopping the currently working service.
func verifyBinary(ctx context.Context, rt runtime.ContainerRuntime, name, path string) error {
	data, err := rt.Exec(ctx, name, []string{path, "version"})
	if err != nil {
		return fmt.Errorf("verify PicoClaw artifact: %w", err)
	}
	var info struct {
		Version    string `json:"version"`
		Customized bool   `json:"customized"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("invalid agent version response: %w", err)
	}
	if !info.Customized || !strings.HasPrefix(info.Version, "picoclaw-") {
		return fmt.Errorf("artifact is not the svkexe PicoClaw integration")
	}
	return nil
}

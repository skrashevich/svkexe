package picoclaw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/runtime"
)

type guestRuntime struct {
	runtime.ContainerRuntime
	commands []string
	files    map[string][]byte
	fail     string
}

func (g *guestRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	text := strings.Join(cmd, " ")
	g.commands = append(g.commands, text)
	if g.fail != "" && strings.Contains(text, g.fail) {
		return nil, errors.New("guest command failed")
	}
	if len(cmd) == 3 && cmd[0] == "sh" && strings.Contains(cmd[2], " | base64 -d > ") {
		encoded := strings.Split(cmd[2], "'")[3]
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		path := strings.Split(cmd[2], " > ")[1]
		g.files[path] = data
	}
	switch {
	case len(cmd) == 2 && cmd[1] == "version":
		return []byte(`{"version":"picoclaw-v0.3.1-svkexe","customized":true}`), nil
	case strings.Contains(text, "is-active"):
		return []byte("active\n"), nil
	case strings.Contains(text, "SELECT 1 FROM sqlite_master"):
		return []byte("1\n"), nil
	case strings.Contains(text, "SELECT model_id"):
		return []byte("svkexe-test/model\n"), nil
	}
	return nil, nil
}
func (g *guestRuntime) PushFile(_ context.Context, _, path string, data []byte) error {
	g.files[path] = data
	return nil
}
func (g *guestRuntime) PullFile(context.Context, string, string) ([]byte, error) {
	return []byte("archive"), nil
}

func TestSetupMigrationAndGatewayConfiguration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	guest := &guestRuntime{files: map[string][]byte{EnvFilePath: []byte("OLD_KEY=stale")}}
	cfg := &LLMProxyConfig{BaseURL: "http://gateway/api/llm/v1", Token: "token'$(not-a-command)\nSEED_EOF", Models: []string{"test/model"}}
	if err := SetupContainer(t.Context(), guest, nil, "id", "vm", "owner", cfg); err != nil {
		t.Fatal(err)
	}
	if string(guest.files["/usr/local/bin/picoclaw.new"]) != "agent-binary" {
		t.Fatal("agent artifact not installed")
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	if config["default_model"] != "svkexe-test/model" || config["llm_gateway"] != "" {
		t.Fatalf("wrong model routing: %v", config)
	}
	if len(guest.files[EnvFilePath]) != 0 {
		t.Fatal("deleted credentials persisted")
	}
	script := strings.Join(guest.commands, "\n")
	if strings.Contains(script, cfg.Token) {
		t.Fatal("token interpolated into shell")
	}
	for _, want := range []string{"shelley.pre-picoclaw.db", "systemctl daemon-reload", "restart picoclaw.service", "http://127.0.0.1:9000/api/models"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing migration step %s", want)
		}
	}
	if !strings.Contains(string(guest.files["/etc/shelley/models.sql"]), sqlQuote(cfg.Token)) {
		t.Fatal("gateway token not seeded losslessly")
	}
	if strings.Index(script, "disable --now shelley.service") > strings.Index(script, "enable picoclaw.service") {
		t.Fatal("old agent stopped after new agent started")
	}
}

func TestSetupRefusesMissingArtifact(t *testing.T) {
	t.Setenv("SVKEXE_AGENT_BINARY", filepath.Join(t.TempDir(), "missing"))
	guest := &guestRuntime{files: map[string][]byte{}}
	if err := SetupContainer(t.Context(), guest, nil, "id", "vm", "owner", nil); err == nil {
		t.Fatal("missing agent reported ready")
	}
	for _, cmd := range guest.commands {
		if strings.Contains(cmd, "disable --now") {
			t.Fatal("old service touched before artifact validation")
		}
	}
}
func TestBackupFailureIsReturned(t *testing.T) {
	guest := &guestRuntime{fail: "tar -czf"}
	if _, err := BackupData(t.Context(), guest, "vm"); err == nil {
		t.Fatal("backup failure ignored")
	}
}
func TestSetupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := SetupContainer(ctx, &guestRuntime{}, nil, "id", "vm", "owner", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestSetupRejectsIncompatibleArtifactBeforeStopping(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	guest := &guestRuntime{files: map[string][]byte{}, fail: "picoclaw.new version"}
	if err := SetupContainer(t.Context(), guest, nil, "id", "vm", "owner", nil); err == nil {
		t.Fatal("incompatible guest binary accepted")
	}
	for _, cmd := range guest.commands {
		if strings.Contains(cmd, "disable --now") {
			t.Fatal("working agent stopped before new binary validation")
		}
	}
}

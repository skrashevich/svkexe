package picoclaw

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type updateGuest struct {
	*guestRuntime
	checksum string
}

func (g *updateGuest) Exec(ctx context.Context, name string, cmd []string) ([]byte, error) {
	if strings.Contains(strings.Join(cmd, " "), "sha256sum /proc/") {
		return []byte(g.checksum + "  /proc/123/exe"), nil
	}
	return g.guestRuntime.Exec(ctx, name, cmd)
}

func TestAgentUpdateUsesPlatformArtifact(t *testing.T) {
	data := []byte("platform-agent")
	path := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", path)
	g := &updateGuest{guestRuntime: &guestRuntime{files: map[string][]byte{}}, checksum: strings.Repeat("0", 64)}
	info, err := CheckUpdate(t.Context(), g, "vm")
	if err != nil || !info.HasUpdate {
		t.Fatalf("check = %+v, %v", info, err)
	}
	g.checksum = fmt.Sprintf("%x", sha256.Sum256(data))
	info, err = CheckUpdate(t.Context(), g, "vm")
	if err != nil || info.HasUpdate {
		t.Fatalf("matching check = %+v, %v", info, err)
	}
	if err := Update(t.Context(), g, "vm"); err != nil {
		t.Fatal(err)
	}
	if string(g.files["/usr/local/bin/picoclaw.new"]) != string(data) {
		t.Fatal("wrong artifact installed")
	}
	cmds := strings.Join(g.commands, "\n")
	if !strings.Contains(cmds, "systemctl restart picoclaw.service") || !strings.Contains(cmds, "/api/models") {
		t.Fatalf("missing service restart/readiness: %s", cmds)
	}
	// No VM lifecycle method is implemented: calling one would panic.
	g.fail = "systemctl restart"
	if err := Update(t.Context(), g, "vm"); err == nil {
		t.Fatal("restart failure swallowed")
	}
}

func TestAgentUpdateMissingArtifactDoesNotRestart(t *testing.T) {
	t.Setenv("SVKEXE_AGENT_BINARY", filepath.Join(t.TempDir(), "missing"))
	g := &guestRuntime{files: map[string][]byte{}}
	if err := Update(t.Context(), g, "vm"); err == nil {
		t.Fatal("missing artifact accepted")
	}
	if len(g.commands) != 0 {
		t.Fatalf("guest changed: %v", g.commands)
	}
}

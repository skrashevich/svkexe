package picoclaw

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/skrashevich/svkexe/internal/runtime"
)

// Execute the actual CLI so its stdout side effects cannot be hidden by a fake.
type sqliteGuestRuntime struct {
	runtime.ContainerRuntime
	path string
}

func (r sqliteGuestRuntime) Exec(ctx context.Context, _ string, cmd []string) ([]byte, error) {
	args := slices.Clone(cmd[1:])
	for i, arg := range args {
		if arg == DBPath {
			args[i] = r.path
		}
	}
	return exec.CommandContext(ctx, cmd[0], args...).CombinedOutput()
}

func TestGuestModelQueriesSQLiteCLI(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI unavailable")
	}
	guest := sqliteGuestRuntime{path: filepath.Join(t.TempDir(), "agent.db")}
	if out, err := exec.CommandContext(t.Context(), "sqlite3", guest.path,
		"CREATE TABLE models(model_id TEXT PRIMARY KEY); INSERT INTO models VALUES ('10000'), ('svkexe-example/model'), ('user-model');").CombinedOutput(); err != nil {
		t.Fatalf("prepare database: %v: %s", err, out)
	}
	for _, tc := range []struct {
		name string
		read func(context.Context, runtime.ContainerRuntime, string) ([]string, error)
		want []string
	}{
		{"all", listGuestModels, []string{"10000", "svkexe-example/model", "user-model"}},
		{"seeded", readSeededModelIDs, []string{"svkexe-example/model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.read(t.Context(), guest, "vm")
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("model IDs = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if out, err := exec.CommandContext(t.Context(), "sqlite3", guest.path, "DELETE FROM models;").CombinedOutput(); err != nil {
		t.Fatalf("empty database: %v: %s", err, out)
	}
	for _, read := range []func(context.Context, runtime.ContainerRuntime, string) ([]string, error){listGuestModels, readSeededModelIDs} {
		if got, err := read(t.Context(), guest, "vm"); err != nil || len(got) != 0 {
			t.Errorf("empty database model IDs = %v, %v", got, err)
		}
	}
}

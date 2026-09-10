package picoclaw

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

// ReconcileRunning migrates existing running VMs when the gateway is updated.
// Stopped VMs receive the same setup through the API/dashboard/SSH start paths.
func ReconcileRunning(ctx context.Context, database *db.DB, rt runtime.ContainerRuntime, m *secrets.Materializer, cfg *LLMProxyConfig) {
	containers, err := database.ListAllContainers()
	if err != nil {
		log.Printf("picoclaw: list VMs for migration: %v", err)
		return
	}
	for _, c := range containers {
		if !strings.EqualFold(c.Status, "running") {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		setupCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := SetupContainer(setupCtx, rt, m, c.ID, c.IncusName, c.OwnerID, cfg)
		cancel()
		if err != nil {
			log.Printf("picoclaw: migration failed for %s: %v", c.IncusName, err)
			_ = database.UpdateContainerStatus(c.ID, "error", c.IPAddress)
		}
	}
}

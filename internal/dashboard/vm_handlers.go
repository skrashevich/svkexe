package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/shelley"
)

// userFromCtx extracts the authenticated *db.User from context.
// Populated by api.AuthMiddleware using ctxkeys.User.
func userFromCtx(ctx context.Context) *dbpkg.User {
	v, _ := ctx.Value(ctxkeys.User).(*dbpkg.User)
	return v
}

// formInt parses a form integer value with a fallback default.
func formInt(r *http.Request, key string, def int) int {
	s := r.FormValue(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// getVMs handles GET /dashboard/vms — full page render.
func (d *Dashboard) getVMs(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	containers, err := d.db.ListContainersByOwner(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := d.newData(r)
	data.Containers = containers
	d.renderPage(w, "vms.html", data)
}

// getVMList handles GET /dashboard/vms/list — htmx partial refresh.
func (d *Dashboard) getVMList(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	containers, err := d.db.ListContainersByOwner(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Backfill missing IPs for running containers from the runtime.
	for _, c := range containers {
		if c.Status == "running" && c.IPAddress == "" {
			if rtc, err := d.runtime.Get(r.Context(), c.IncusName); err == nil && rtc.IP != "" {
				c.IPAddress = rtc.IP
				_ = d.db.UpdateContainerStatus(c.ID, c.Status, rtc.IP)
			}
		}
	}

	data := d.newData(r)
	data.Containers = containers
	d.render(w, "vm_list_content", data)
}

// getVMCreate handles GET /dashboard/vms/create — returns create form partial.
func (d *Dashboard) getVMCreate(w http.ResponseWriter, r *http.Request) {
	data := d.newData(r)
	d.render(w, "vm_create_form", data)
}

// postCreateVM handles POST /dashboard/vms — creates a container.
func (d *Dashboard) postCreateVM(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	cpuLimit := formInt(r, "cpu_limit", 2)
	memoryMB := formInt(r, "memory_mb", 2048)
	diskGB := formInt(r, "disk_gb", 10)

	if existing, _ := d.db.GetContainerByName(name, user.ID); existing != nil {
		http.Error(w, "VM with this name already exists", http.StatusConflict)
		return
	}

	incusName := fmt.Sprintf("svkexe-%s-%s", user.ID, name)

	c := &dbpkg.Container{
		ID:        uuid.New().String(),
		Name:      name,
		OwnerID:   user.ID,
		IncusName: incusName,
		Status:    "creating",
		CPULimit:  cpuLimit,
		MemoryMB:  memoryMB,
		DiskGB:    diskGB,
	}
	if err := d.db.CreateContainer(c); err != nil {
		http.Error(w, "failed to persist VM", http.StatusInternalServerError)
		return
	}

	// Create the Incus container asynchronously — the UI polls every 5s and
	// will pick up the status change from "creating" to "stopped".
	go func() {
		ctx := context.Background()
		rtContainer, err := d.runtime.Create(ctx, runtime.CreateOpts{
			Name:     name,
			OwnerID:  user.ID,
			Image:    shelley.DefaultImage,
			CPULimit: cpuLimit,
			MemoryMB: memoryMB,
			DiskGB:   diskGB,
		})
		if err != nil {
			log.Printf("async VM create failed for %s: %v", incusName, err)
			_ = d.db.UpdateContainerStatus(c.ID, "error", "")
			return
		}
		_ = d.db.UpdateContainerStatus(c.ID, rtContainer.Status, rtContainer.IP)

		if d.materializer != nil {
			if err := shelley.SetupContainer(ctx, d.runtime, d.materializer, c.ID, user.ID, d.shelleyLLMCfg); err != nil {
				log.Printf("shelley setup failed for %s: %v", incusName, err)
			}
		}
	}()

	// Return updated VM list immediately (VM visible with "creating" badge).
	containers, err := d.db.ListContainersByOwner(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := d.newData(r)
	data.Containers = containers
	d.render(w, "vm_list_content", data)
}

// postStartVM handles POST /dashboard/vms/{id}/start.
func (d *Dashboard) postStartVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if d.materializer != nil {
		if err := d.materializer.RefreshKeys(id, c.OwnerID); err != nil {
			http.Error(w, "failed to refresh keys: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := d.runtime.Start(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to start VM: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch fresh IP from runtime after start.
	if rtc, err := d.runtime.Get(r.Context(), c.IncusName); err == nil {
		c.IPAddress = rtc.IP
	}

	if err := d.db.UpdateContainerStatus(id, "running", c.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}

	c.Status = "running"
	d.render(w, "vm_card", c)
}

// postStopVM handles POST /dashboard/vms/{id}/stop.
func (d *Dashboard) postStopVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := d.runtime.Stop(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to stop VM: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.db.UpdateContainerStatus(id, "stopped", c.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}

	c.Status = "stopped"
	d.render(w, "vm_card", c)
}

// deleteVM handles DELETE /dashboard/vms/{id}.
func (d *Dashboard) deleteVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if c.Status == "creating" {
		http.Error(w, "VM is still being created, please wait", http.StatusConflict)
		return
	}

	if c.Status == "running" {
		if err := d.runtime.Stop(r.Context(), c.IncusName); err != nil {
			http.Error(w, "failed to stop VM before deletion: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := d.runtime.Delete(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to delete VM: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.db.DeleteContainer(id); err != nil {
		http.Error(w, "failed to remove VM record", http.StatusInternalServerError)
		return
	}
	if d.materializer != nil {
		_ = d.materializer.RemoveKeys(id)
	}

	w.WriteHeader(http.StatusOK)
}

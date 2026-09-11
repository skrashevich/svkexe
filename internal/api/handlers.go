package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/vmconfig"
)

// containerIDFromURL returns the {id} URL parameter.
func containerIDFromURL(r *http.Request) string {
	return chi.URLParam(r, "id")
}

func validContainerName(name string) bool {
	return dbpkg.ValidContainerName(name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// listContainers handles GET /api/containers
func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	containers, err := s.db.ListContainersByOwner(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Without this the serialised NestingAllowed is the zero value, so every VM
	// would report the capability as forbidden regardless of the real setting.
	if err := s.db.AttachNestingPolicy(containers...); err != nil {
		log.Printf("list containers for %s: attach nesting policy: %v", userID, err)
	}
	writeJSON(w, http.StatusOK, containers)
}

// createContainerRequest is the JSON body for container creation.
type createContainerRequest struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	CPULimit int    `json:"cpu_limit"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	// InitialTask is handed to the agent once the VM is up.
	InitialTask string `json:"initial_task"`
	// Nesting decides whether the VM may run containers of its own. A body that
	// omits it gets the platform default rather than Go's zero value: a caller
	// that never heard of the field must not silently create a VM where Docker
	// cannot start.
	Nesting *bool `json:"nesting"`
}

// createContainer handles POST /api/containers
func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())

	var req createContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if !validContainerName(req.Name) {
		http.Error(w, "invalid container name: use lowercase letters, digits, and hyphens (2-63 chars); names may not start with \"agent-\" or a port prefix like \"3000-\"", http.StatusBadRequest)
		return
	}
	if _, err := s.db.GetContainerByName(req.Name, userID); err == nil {
		http.Error(w, "container name already exists", http.StatusConflict)
		return
	} else if err != nil && err != sql.ErrNoRows {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if req.CPULimit == 0 {
		req.CPULimit = 2
	}
	if req.Image == "" {
		req.Image = picoclaw.DefaultImage
	}
	if req.MemoryMB == 0 {
		req.MemoryMB = 2048
	}
	if req.DiskGB == 0 {
		req.DiskGB = 10
	}

	nestingAllowed, err := s.db.NestingAllowed()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nesting := dbpkg.DefaultNesting
	if req.Nesting != nil {
		nesting = *req.Nesting
	}
	effectiveNesting := nestingAllowed && nesting

	rtContainer, err := s.runtime.Create(r.Context(), runtime.CreateOpts{
		Name:     req.Name,
		OwnerID:  userID,
		Image:    req.Image,
		CPULimit: req.CPULimit,
		MemoryMB: req.MemoryMB,
		DiskGB:   req.DiskGB,
		Nesting:  effectiveNesting,
	})
	if err != nil {
		http.Error(w, "failed to create container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	dbContainer := &dbpkg.Container{
		ID:        uuid.New().String(),
		Name:      req.Name,
		OwnerID:   userID,
		IncusName: rtContainer.Name,
		Status:    rtContainer.Status,
		IPAddress: rtContainer.IP,
		CPULimit:  req.CPULimit,
		MemoryMB:  req.MemoryMB,
		DiskGB:    req.DiskGB,
		Nesting:   nesting,

		InitialTask: req.InitialTask,
	}
	if err := s.db.CreateContainer(dbContainer); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "container name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to persist container", http.StatusInternalServerError)
		return
	}

	// The instance was built with the setting already on it, so the start below
	// is what puts it in effect.
	if err := vmconfig.MarkStarted(s.db, dbContainer, effectiveNesting); err != nil {
		log.Printf("create %s: record nesting: %v", rtContainer.Name, err)
	}

	// Incus hands back a stopped instance, so bring it up as part of creation —
	// a new VM is expected to be usable without a separate start call.
	if !strings.EqualFold(rtContainer.Status, "running") {
		if err := s.runtime.Start(r.Context(), rtContainer.Name); err != nil {
			_ = s.db.UpdateContainerStatus(dbContainer.ID, "stopped", dbContainer.IPAddress)
			http.Error(w, "failed to start container: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if rtc, err := s.runtime.Get(r.Context(), rtContainer.Name); err == nil && rtc != nil {
		dbContainer.IPAddress = rtc.IP
	}

	if s.materializer != nil {
		if err := picoclaw.SetupContainer(r.Context(), s.runtime, s.db, s.materializer, dbContainer, s.picoclawLLMCfg); err != nil {
			_ = s.db.UpdateContainerStatus(dbContainer.ID, "error", dbContainer.IPAddress)
			http.Error(w, "PicoClaw setup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		picoclaw.DeliverInitialTaskByID(r.Context(), s.runtime, s.db, dbContainer.ID, s.picoclawLLMCfg)
	}

	dbContainer.Status = "running"
	if err := s.db.UpdateContainerStatus(dbContainer.ID, "running", dbContainer.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, dbContainer)
}

// getContainer handles GET /api/containers/{id}
func (s *Server) getContainer(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	c, err := s.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.AttachNestingPolicy(c); err != nil {
		log.Printf("get container %s: attach nesting policy: %v", id, err)
	}
	writeJSON(w, http.StatusOK, c)
}

// deleteContainer handles DELETE /api/containers/{id}
func (s *Server) deleteContainer(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	c, err := s.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.runtime.Delete(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to delete container: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.DeleteContainer(id); err != nil {
		http.Error(w, "failed to remove container record", http.StatusInternalServerError)
		return
	}

	if s.materializer != nil {
		_ = s.materializer.RemoveKeys(id)
	}

	w.WriteHeader(http.StatusNoContent)
}

// recreateContainer handles POST /api/containers/{id}/recreate
// It redeploys the container from the latest base image while preserving /data.
func (s *Server) recreateContainer(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	c, err := s.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if c.Status == "creating" || c.Status == "recreating" {
		http.Error(w, "VM is busy, please wait", http.StatusConflict)
		return
	}

	_ = s.db.UpdateContainerStatus(id, "recreating", c.IPAddress)
	w.WriteHeader(http.StatusAccepted)

	go s.doRecreate(c)
}

// doRecreate performs the actual recreate workflow in a background goroutine.
func (s *Server) doRecreate(c *dbpkg.Container) {
	ctx := context.Background()
	id := c.ID

	// The old VM must stay intact unless its data was successfully backed up.
	// It is a real boot, running for as long as the backup takes, so it has to
	// honour the current setting like any other.
	if err := vmconfig.PrepareStart(ctx, s.runtime, s.db, c); err != nil {
		log.Printf("recreate: apply nesting before backup for %s: %v", c.IncusName, err)
	}
	if err := s.runtime.Start(ctx, c.IncusName); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}
	backupData, err := picoclaw.BackupData(ctx, s.runtime, c.IncusName)
	if err != nil {
		log.Printf("backup agent data: %v", err)
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}
	if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}

	// Delete old container.
	if err := s.runtime.Delete(ctx, c.IncusName); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}

	// Create new container from fresh image with same settings. The rebuild is
	// the VM's chance to land on the current nesting setting, so it is resolved
	// again rather than copied from what the old instance booted with.
	effectiveNesting, err := vmconfig.EffectiveNesting(s.db, c)
	if err != nil {
		log.Printf("recreate: resolve nesting for %s: %v", c.IncusName, err)
	}
	if _, err := s.runtime.Create(ctx, runtime.CreateOpts{
		Name:     c.Name,
		OwnerID:  c.OwnerID,
		Image:    picoclaw.DefaultImage,
		CPULimit: c.CPULimit,
		MemoryMB: c.MemoryMB,
		DiskGB:   c.DiskGB,
		Nesting:  effectiveNesting,
	}); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}
	if err := vmconfig.MarkStarted(s.db, c, effectiveNesting); err != nil {
		log.Printf("recreate: record nesting for %s: %v", c.IncusName, err)
	}

	// Start the new container.
	if err := s.runtime.Start(ctx, c.IncusName); err != nil {
		_ = s.db.UpdateContainerStatus(id, "stopped", "")
		return
	}

	// Fetch new IP.
	ip := ""
	if rtc, err := s.runtime.Get(ctx, c.IncusName); err == nil {
		ip = rtc.IP
	}

	// Restore the database before PicoClaw opens it; then apply current keys/models.
	if err := picoclaw.RestoreData(ctx, s.runtime, c.IncusName, backupData); err != nil {
		log.Printf("restore agent data: %v", err)
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}
	if err := picoclaw.SetupContainer(ctx, s.runtime, s.db, s.materializer, c, s.picoclawLLMCfg); err != nil {
		log.Printf("recreate: PicoClaw setup: %v", err)
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}

	_ = s.db.UpdateContainerStatus(id, "running", ip)
}

// startContainer handles POST /api/containers/{id}/start
func (s *Server) startContainer(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	c, err := s.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.materializer != nil {
		if err := s.materializer.RefreshKeys(id, c.OwnerID); err != nil {
			http.Error(w, "failed to refresh keys: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// The runtime only reads the nesting setting at boot, so this is the moment a
	// pending change becomes real. Failing to write it must not block the start:
	// the VM comes up on its previous setting and still owes a restart.
	if err := vmconfig.PrepareStart(r.Context(), s.runtime, s.db, c); err != nil {
		log.Printf("start %s: apply nesting: %v", c.IncusName, err)
	}

	if err := s.runtime.Start(r.Context(), c.IncusName); err != nil {
		// A row left saying "running" after a start that did not happen would have
		// the dashboard speak for a VM that is down.
		_ = s.db.UpdateContainerStatus(id, "stopped", "")
		http.Error(w, "failed to start container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-apply PicoClaw config (env files, systemd unit) on every start so
	// containers created before a config change pick up the new values.
	if s.materializer != nil {
		setupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := picoclaw.SetupContainer(setupCtx, s.runtime, s.db, s.materializer, c, s.picoclawLLMCfg); err != nil {
			log.Printf("PicoClaw setup on start %s: %v", c.IncusName, err)
			_ = s.db.UpdateContainerStatus(id, "error", c.IPAddress)
			http.Error(w, "PicoClaw setup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		picoclaw.DeliverInitialTaskByID(setupCtx, s.runtime, s.db, id, s.picoclawLLMCfg)
	}

	// Fetch fresh IP from runtime after start.
	if rtc, err := s.runtime.Get(r.Context(), c.IncusName); err == nil {
		c.IPAddress = rtc.IP
	}

	if err := s.db.UpdateContainerStatus(id, "running", c.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stopContainer handles POST /api/containers/{id}/stop
func (s *Server) stopContainer(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	c, err := s.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.runtime.Stop(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to stop container: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateContainerStatus(id, "stopped", c.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/shelley"
)

// containerIDFromURL returns the {id} URL parameter.
func containerIDFromURL(r *http.Request) string {
	return chi.URLParam(r, "id")
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
	writeJSON(w, http.StatusOK, containers)
}

// createContainerRequest is the JSON body for container creation.
type createContainerRequest struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	CPULimit int    `json:"cpu_limit"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
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
	if req.CPULimit == 0 {
		req.CPULimit = 2
	}
	if req.MemoryMB == 0 {
		req.MemoryMB = 2048
	}
	if req.DiskGB == 0 {
		req.DiskGB = 10
	}

	rtContainer, err := s.runtime.Create(r.Context(), runtime.CreateOpts{
		Name:     req.Name,
		OwnerID:  userID,
		Image:    req.Image,
		CPULimit: req.CPULimit,
		MemoryMB: req.MemoryMB,
		DiskGB:   req.DiskGB,
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
	}
	if err := s.db.CreateContainer(dbContainer); err != nil {
		http.Error(w, "failed to persist container", http.StatusInternalServerError)
		return
	}

	if s.materializer != nil {
		if err := shelley.SetupContainer(r.Context(), s.runtime, s.materializer, dbContainer.ID, userID, s.shelleyLLMCfg); err != nil {
			// Non-fatal: container is created, log and continue.
			_ = err
		}
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

	// Start the container if stopped so we can back up /data.
	wasRunning := c.Status == "running"
	if c.Status == "stopped" {
		if err := s.runtime.Start(ctx, c.IncusName); err == nil {
			wasRunning = true
		}
	}

	// Back up /data.
	var backupData []byte
	if wasRunning {
		if fr, ok := s.runtime.(runtime.FileRuntime); ok {
			if _, err := s.runtime.Exec(ctx, c.IncusName, []string{"tar", "-czf", "/tmp/data-backup.tar.gz", "-C", "/", "data"}); err == nil {
				backupData, _ = fr.PullFile(ctx, c.IncusName, "/tmp/data-backup.tar.gz")
			}
		}
	}

	// Stop the container.
	if wasRunning {
		if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
			_ = s.db.UpdateContainerStatus(id, "error", "")
			return
		}
	}

	// Delete old container.
	if err := s.runtime.Delete(ctx, c.IncusName); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}

	// Create new container from fresh image with same settings.
	if _, err := s.runtime.Create(ctx, runtime.CreateOpts{
		Name:     c.Name,
		OwnerID:  c.OwnerID,
		Image:    shelley.DefaultImage,
		CPULimit: c.CPULimit,
		MemoryMB: c.MemoryMB,
		DiskGB:   c.DiskGB,
	}); err != nil {
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
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

	// Run shelley setup.
	if s.materializer != nil {
		_ = shelley.SetupContainer(ctx, s.runtime, s.materializer, id, c.OwnerID, s.shelleyLLMCfg)
	}

	// Restore /data backup.
	if len(backupData) > 0 {
		if fr, ok := s.runtime.(runtime.FileRuntime); ok {
			if err := fr.PushFile(ctx, c.IncusName, "/tmp/data-backup.tar.gz", backupData); err == nil {
				s.runtime.Exec(ctx, c.IncusName, []string{"tar", "-xzf", "/tmp/data-backup.tar.gz", "-C", "/"})
				s.runtime.Exec(ctx, c.IncusName, []string{"rm", "-f", "/tmp/data-backup.tar.gz"})
				s.runtime.Exec(ctx, c.IncusName, []string{"chown", "-R", "user:user", "/data"})
			}
		}
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

	if err := s.runtime.Start(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to start container: "+err.Error(), http.StatusInternalServerError)
		return
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

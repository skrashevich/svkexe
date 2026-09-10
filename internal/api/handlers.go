package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// containerIDFromURL returns the {id} URL parameter.
func containerIDFromURL(r *http.Request) string {
	return chi.URLParam(r, "id")
}

var containerNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validContainerName(name string) bool {
	return len(name) >= 2 && len(name) <= 63 && containerNameRE.MatchString(name)
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
	if !validContainerName(req.Name) {
		http.Error(w, "invalid container name: use lowercase letters, digits, and hyphens (2-63 chars)", http.StatusBadRequest)
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
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "container name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to persist container", http.StatusInternalServerError)
		return
	}

	if strings.EqualFold(rtContainer.Status, "running") {
		if err := picoclaw.SetupContainer(r.Context(), s.runtime, s.materializer, dbContainer.ID, dbContainer.IncusName, userID, s.picoclawLLMCfg); err != nil {
			_ = s.db.UpdateContainerStatus(dbContainer.ID, "error", dbContainer.IPAddress)
			http.Error(w, "PicoClaw setup failed: "+err.Error(), http.StatusInternalServerError)
			return
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

	// The old VM must stay intact unless its data was successfully backed up.
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

	// Create new container from fresh image with same settings.
	if _, err := s.runtime.Create(ctx, runtime.CreateOpts{
		Name:     c.Name,
		OwnerID:  c.OwnerID,
		Image:    picoclaw.DefaultImage,
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

	// Restore the database before PicoClaw opens it; then apply current keys/models.
	if err := picoclaw.RestoreData(ctx, s.runtime, c.IncusName, backupData); err != nil {
		log.Printf("restore agent data: %v", err)
		_ = s.db.UpdateContainerStatus(id, "error", "")
		return
	}
	if err := picoclaw.SetupContainer(ctx, s.runtime, s.materializer, id, c.IncusName, c.OwnerID, s.picoclawLLMCfg); err != nil {
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

	if err := s.runtime.Start(r.Context(), c.IncusName); err != nil {
		http.Error(w, "failed to start container: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-apply PicoClaw config (env files, systemd unit) on every start so
	// containers created before a config change pick up the new values.
	if s.materializer != nil {
		setupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := picoclaw.SetupContainer(setupCtx, s.runtime, s.materializer, id, c.IncusName, c.OwnerID, s.picoclawLLMCfg); err != nil {
			log.Printf("PicoClaw setup on start %s: %v", c.IncusName, err)
			_ = s.db.UpdateContainerStatus(id, "error", c.IPAddress)
			http.Error(w, "PicoClaw setup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
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

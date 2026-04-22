package api

import (
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

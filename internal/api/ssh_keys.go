package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

type sshKeyResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
}

// listSSHKeys handles GET /api/ssh-keys
func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	keys, err := s.db.ListSSHKeysByUser(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := make([]sshKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, sshKeyResponse{
			ID:          k.ID,
			Name:        k.Name,
			Fingerprint: k.Fingerprint,
			CreatedAt:   k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// createSSHKey handles POST /api/ssh-keys
func (s *Server) createSSHKey(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PublicKey = strings.TrimSpace(req.PublicKey)

	if req.Name == "" || req.PublicKey == "" {
		http.Error(w, "name and public_key are required", http.StatusBadRequest)
		return
	}

	pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil {
		http.Error(w, "invalid public key format", http.StatusBadRequest)
		return
	}

	fingerprint := gossh.FingerprintSHA256(pubKey)

	key := &db.SSHKey{
		ID:          uuid.New().String(),
		UserID:      user.ID,
		Fingerprint: fingerprint,
		PublicKey:   req.PublicKey,
		Name:        req.Name,
	}

	if err := s.db.CreateSSHKey(key); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "key already exists", http.StatusConflict)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(sshKeyResponse{
		ID:          key.ID,
		Name:        key.Name,
		Fingerprint: fingerprint,
		CreatedAt:   key.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// deleteSSHKey handles DELETE /api/ssh-keys/{id}
func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if err := s.db.DeleteSSHKey(id, user.ID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

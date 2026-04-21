package dashboard

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/svkexe/platform/internal/ctxkeys"
	"github.com/svkexe/platform/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

// getSSHKeys handles GET /dashboard/ssh-keys
func (d *Dashboard) getSSHKeys(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	keys, err := d.db.ListSSHKeysByUser(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		User    *db.User
		Domain  string
		SSHKeys []*db.SSHKey
		Error   string
	}{
		User:    user,
		Domain:  d.domain,
		SSHKeys: keys,
	}
	d.renderPage(w, "ssh_keys.html", data)
}

// postSSHKey handles POST /dashboard/ssh-keys
func (d *Dashboard) postSSHKey(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	publicKey := strings.TrimSpace(r.FormValue("public_key"))

	renderError := func(msg string) {
		keys, _ := d.db.ListSSHKeysByUser(user.ID)
		data := struct {
			User    *db.User
			Domain  string
			SSHKeys []*db.SSHKey
			Error   string
		}{
			User:    user,
			Domain:  d.domain,
			SSHKeys: keys,
			Error:   msg,
		}
		d.renderPage(w, "ssh_keys.html", data)
	}

	if name == "" || publicKey == "" {
		renderError("Name and public key are required.")
		return
	}

	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		renderError("Invalid public key format. Paste in authorized_keys format (e.g. ssh-ed25519 AAAA...).")
		return
	}

	fingerprint := gossh.FingerprintSHA256(pub)

	key := &db.SSHKey{
		ID:          uuid.New().String(),
		UserID:      user.ID,
		Fingerprint: fingerprint,
		PublicKey:   publicKey,
		Name:        name,
	}

	if err := d.db.CreateSSHKey(key); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			renderError("This SSH key is already registered.")
			return
		}
		renderError("Failed to save key.")
		return
	}

	http.Redirect(w, r, "/dashboard/ssh-keys", http.StatusSeeOther)
}

// deleteSSHKey handles DELETE /dashboard/ssh-keys/{id}
func (d *Dashboard) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(ctxkeys.User).(*db.User)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if err := d.db.DeleteSSHKey(id, user.ID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// For htmx: return empty 200 so the row is swapped out.
	w.WriteHeader(http.StatusOK)
}

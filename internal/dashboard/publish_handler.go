package dashboard

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
)

// postPublish handles POST /dashboard/vms/{id}/publish and re-renders the card.
func (d *Dashboard) postPublish(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("app_port")))
	if err != nil || !dbpkg.ValidAppPort(port) {
		http.Error(w, "Port must be between 1 and 65535 and must not be the agent port", http.StatusBadRequest)
		return
	}
	// An unchecked checkbox is simply absent from the form, which is what makes
	// "private" the value a user lands on by unchecking it.
	public := r.FormValue("app_public") != ""

	if err := d.db.UpdateContainerPublish(id, port, public); err != nil {
		http.Error(w, "Failed to save publish settings", http.StatusInternalServerError)
		return
	}

	updated, err := d.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The agent is told which port to serve on; a stale answer would have it
	// configure software for a port that is no longer published. Not reaching
	// the VM must not fail the setting the owner just saved.
	if err := picoclaw.RefreshEnvironmentGuide(r.Context(), d.runtime, d.db, updated); err != nil {
		log.Printf("publish settings for %s: refresh agent guide: %v", updated.IncusName, err)
	}
	d.renderCard(w, updated)
}

// postRetryTask handles POST /dashboard/vms/{id}/task/retry and re-renders the card.
func (d *Dashboard) postRetryTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := d.db.RetryInitialTask(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	picoclaw.DeliverInitialTaskByID(r.Context(), d.runtime, d.db, id, d.picoclawLLMCfg)

	updated, err := d.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.renderCard(w, updated)
}

package dashboard

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/vmconfig"
)

// ownedVM resolves the {id} URL parameter to a VM the caller owns. The
// dashboard router runs behind session auth but not behind the API's ownership
// middleware, so the check belongs to each handler that touches a VM.
func (d *Dashboard) ownedVM(w http.ResponseWriter, r *http.Request) *dbpkg.Container {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "VM not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return c
}

// postNesting handles POST /dashboard/vms/{id}/nesting and re-renders the card.
// The owner decides this one: nested containers are what make Docker, buildah
// and every image-based build work inside the VM, so it is an ordinary
// capability of their own machine rather than a privilege. What they cannot do
// is escape the deployment-wide ceiling.
func (d *Dashboard) postNesting(w http.ResponseWriter, r *http.Request) {
	c := d.ownedVM(w, r)
	if c == nil {
		return
	}

	allowed, err := d.db.NestingAllowed()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		// The checkbox is rendered disabled in this state, so reaching here means
		// the request did not come from the rendered form.
		http.Error(w, "Nested containers are disabled for this deployment", http.StatusForbidden)
		return
	}

	// An unchecked checkbox is simply absent from the form, which is what makes
	// "off" the value an owner lands on by unchecking it.
	nesting := r.FormValue("nesting") != ""
	if err := d.db.UpdateContainerNesting(c.ID, nesting); err != nil {
		http.Error(w, "Failed to save the nesting setting", http.StatusInternalServerError)
		return
	}

	updated, err := d.db.GetContainerByID(c.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Write it to the instance now so that a start from anywhere — the SSH menu,
	// the REST API, a host reboot — picks it up even if it never goes through
	// this gateway path again. Not reaching Incus must not lose the owner's
	// choice: it is already stored, and the next start re-applies it.
	if err := vmconfig.ApplyNesting(r.Context(), d.runtime, d.db, updated); err != nil {
		log.Printf("nesting for %s: apply to runtime: %v", updated.IncusName, err)
	}
	// The agent is told whether it can build with Docker; a stale answer sends it
	// down the "compile everything from source" path for no reason.
	if err := picoclaw.RefreshEnvironmentGuide(r.Context(), d.runtime, d.db, updated); err != nil {
		log.Printf("nesting for %s: refresh agent guide: %v", updated.IncusName, err)
	}
	d.renderCard(w, updated)
}

// postRestartVM handles POST /dashboard/vms/{id}/restart. It exists for
// settings that only the runtime's own boot can apply — nesting is the first —
// so the owner does not have to press Stop, wait, and then find Start.
func (d *Dashboard) postRestartVM(w http.ResponseWriter, r *http.Request) {
	c := d.ownedVM(w, r)
	if c == nil {
		return
	}
	if c.Status == "creating" || c.Status == "recreating" {
		http.Error(w, "VM is busy, please wait", http.StatusConflict)
		return
	}

	if err := d.runtime.Stop(r.Context(), c.IncusName); err != nil {
		// A VM that is already down is exactly where a restart wants it.
		if !strings.Contains(err.Error(), "already stopped") {
			http.Error(w, "failed to stop VM: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if d.materializer != nil {
		if err := d.materializer.RefreshKeys(c.ID, c.OwnerID); err != nil {
			http.Error(w, "failed to refresh keys: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := vmconfig.PrepareStart(r.Context(), d.runtime, d.db, c); err != nil {
		log.Printf("restart %s: apply nesting: %v", c.IncusName, err)
	}
	if err := d.runtime.Start(r.Context(), c.IncusName); err != nil {
		_ = d.db.UpdateContainerStatus(c.ID, "stopped", "")
		http.Error(w, "failed to start VM: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if d.materializer != nil {
		setupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := picoclaw.SetupContainer(setupCtx, d.runtime, d.db, d.materializer, c, d.picoclawLLMCfg); err != nil {
			log.Printf("restart: PicoClaw setup failed for %s: %v", c.IncusName, err)
			_ = d.db.UpdateContainerStatus(c.ID, "error", c.IPAddress)
			http.Error(w, "PicoClaw setup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if rtc, err := d.runtime.Get(r.Context(), c.IncusName); err == nil && rtc != nil {
		c.IPAddress = rtc.IP
	}
	if err := d.db.UpdateContainerStatus(c.ID, "running", c.IPAddress); err != nil {
		http.Error(w, "failed to update status", http.StatusInternalServerError)
		return
	}
	c.Status = "running"
	d.renderCard(w, c)
}

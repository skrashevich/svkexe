package dashboard

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/aliases"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// aliasContainer re-reads the VM a /vms/{id}/aliases* route is scoped to. The
// dashboard has no OwnershipMiddleware of its own, so every handler here
// re-derives ownership from the session user the way postPublish does.
func (d *Dashboard) aliasContainer(w http.ResponseWriter, r *http.Request) (*dbpkg.Container, bool) {
	id := chi.URLParam(r, "id")
	user := userFromCtx(r.Context())

	c, err := d.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return nil, false
	}
	if user == nil || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return c, true
}

// writeAliasError maps the manager's failures onto a status and body an owner
// can act on through htmx, mirroring the REST API's mapping in
// internal/api/aliases.go.
func writeAliasError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aliases.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, dbpkg.ErrAliasHostnameTaken), errors.Is(err, dbpkg.ErrAliasAlreadyOnVM):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, aliases.ErrNotFound):
		http.Error(w, "alias not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// postAddAlias handles POST /dashboard/vms/{id}/aliases and re-renders the
// card. A hostname whose DNS does not point here yet is still stored — see
// aliases.Manager.Add — so a failed check does not stand in the way of the
// re-render; the card just shows it unverified.
func (d *Dashboard) postAddAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := d.aliasContainer(w, r)
	if !ok {
		return
	}
	if _, err := d.aliases.Add(r.Context(), c, r.FormValue("hostname")); err != nil {
		writeAliasError(w, err)
		return
	}
	d.renderCard(w, c)
}

// postVerifyAlias handles POST /dashboard/vms/{id}/aliases/{aliasID}/verify
// and re-renders the card. This is how an owner confirms a DNS record they
// just fixed — or discovers a record that stopped pointing here.
func (d *Dashboard) postVerifyAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := d.aliasContainer(w, r)
	if !ok {
		return
	}
	if _, err := d.aliases.Recheck(r.Context(), c, chi.URLParam(r, "aliasID")); err != nil {
		writeAliasError(w, err)
		return
	}
	d.renderCard(w, c)
}

// deleteAlias handles DELETE /dashboard/vms/{id}/aliases/{aliasID} and
// re-renders the card without the removed alias, freeing the hostname for
// reuse.
func (d *Dashboard) deleteAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := d.aliasContainer(w, r)
	if !ok {
		return
	}
	if err := d.aliases.Remove(r.Context(), c, chi.URLParam(r, "aliasID")); err != nil {
		writeAliasError(w, err)
		return
	}
	d.renderCard(w, c)
}

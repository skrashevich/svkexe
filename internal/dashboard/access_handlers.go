package dashboard

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/db"
)

func (d *Dashboard) accessOwner(w http.ResponseWriter, r *http.Request) *db.Container {
	user := userFromCtx(r.Context())
	c, err := d.db.GetContainerByID(chi.URLParam(r, "id"))
	if err != nil || user == nil || user.Role == db.GuestRole || c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	return c
}

// accessTab is where every access action lands: the Access tab of the VM page.
func accessTab(c *db.Container) string {
	return "/dashboard/vms/" + c.ID + "#access"
}

// getAccess handles GET /dashboard/vms/{id}/access. Access moved into a tab of
// the VM page; the old address keeps working for bookmarks and links.
func (d *Dashboard) getAccess(w http.ResponseWriter, r *http.Request) {
	if c := d.accessOwner(w, r); c != nil {
		http.Redirect(w, r, accessTab(c), http.StatusFound)
	}
}
func (d *Dashboard) postAccess(w http.ResponseWriter, r *http.Request) {
	if !accessSameOrigin(w, r) {
		return
	}
	c := d.accessOwner(w, r)
	if c == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if err := d.db.GrantContainerAccess(c.OwnerID, c.ID, r.PostFormValue("email"), r.PostFormValue("public_key")); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		d.renderVMPage(w, r, c, err.Error(), r.PostFormValue("email"))
		return
	}
	http.Redirect(w, r, accessTab(c), http.StatusSeeOther)
}
func (d *Dashboard) postRevokeAccess(w http.ResponseWriter, r *http.Request) {
	if !accessSameOrigin(w, r) {
		return
	}
	c := d.accessOwner(w, r)
	if c == nil {
		return
	}
	if err := d.db.RevokeContainerAccess(c.OwnerID, c.ID, chi.URLParam(r, "userID")); err != nil {
		http.Error(w, "could not revoke access", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, accessTab(c), http.StatusSeeOther)
}

func accessSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "cross-origin request forbidden", http.StatusForbidden)
			return false
		}
	}
	if origin == "" && (r.Header.Get("Sec-Fetch-Site") == "cross-site" || r.Header.Get("Sec-Fetch-Site") == "same-site") {
		http.Error(w, "cross-origin request forbidden", http.StatusForbidden)
		return false
	}
	return true
}

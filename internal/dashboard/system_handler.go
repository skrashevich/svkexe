package dashboard

import (
	"net/http"

	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// systemPageData backs the System page and both of its htmx fragments. It
// embeds templateData so the layout keeps working (.User, .IsAdmin) when the
// same struct is handed to a full-page render.
type systemPageData struct {
	templateData

	// Version is the build metadata of the running binary.
	Version version.Info
	// Status is the result of the last update check, nil until the operator
	// presses the button or when the check failed.
	Status *updater.Status
	// Run is the progress of the most recent update, StateIdle when none ran.
	Run updater.RunState
	// UpdateAvailable mirrors Status.UpdateAvailable so the template does not
	// have to guard against a nil Status to ask the question.
	UpdateAvailable bool
	// CanUpdate reports whether this deployment can install updates itself.
	CanUpdate bool
	// UnavailableReason explains a false CanUpdate in operator terms.
	UnavailableReason string
	// CheckError is the failure of the update check, rendered in place of a
	// result. The check runs inside an htmx fragment, so a failure is content,
	// not an HTTP error.
	CheckError string
	// StartError is the refusal returned when an update could not be started —
	// unsupported deployment, or one already running.
	StartError string
	// Checked distinguishes "no update available" from "never looked".
	Checked bool
}

// requireAdmin gates the system routes. The dashboard router runs behind the
// session auth middleware but not behind the API's AdminMiddleware, so the role
// has to be enforced by each handler that needs it.
func (d *Dashboard) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := userFromCtx(r.Context())
	if user == nil || user.Role != "admin" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// newSystemData collects everything the page and the fragments render from.
// A nil updater is a supported deployment, not a bug: the page then reports
// versions and explains that it cannot check or install anything.
func (d *Dashboard) newSystemData(r *http.Request) systemPageData {
	data := systemPageData{
		templateData: d.newData(r),
		Version:      version.Get(),
		Run:          updater.RunState{State: updater.StateIdle},
	}
	if d.updater == nil {
		data.UnavailableReason = "this deployment was started without the update service"
		return data
	}

	data.Version = d.updater.Version()
	data.CanUpdate, data.UnavailableReason = d.updater.Available()
	st, err := d.updater.State()
	if err != nil {
		// An unreadable status file says nothing about the update itself, so it
		// is surfaced as a failed run rather than swallowed into "idle".
		data.Run = updater.RunState{State: updater.StateFailed, Error: err.Error()}
		return data
	}
	data.Run = st
	return data
}

// getSystem handles GET /dashboard/system — full page render.
func (d *Dashboard) getSystem(w http.ResponseWriter, r *http.Request) {
	if !d.requireAdmin(w, r) {
		return
	}
	d.renderPage(w, "system.html", d.newSystemData(r))
}

// getUpdateCheck handles GET /dashboard/system/check — the htmx fragment
// holding the result of a GitHub lookup.
func (d *Dashboard) getUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !d.requireAdmin(w, r) {
		return
	}
	data := d.newSystemData(r)
	data.Checked = true

	if d.updater == nil {
		data.CheckError = data.UnavailableReason
		d.render(w, "update_check", data)
		return
	}
	// The page auto-loads this fragment, which must stay cheap; an explicit
	// click asks for ?force=1, because answering a deliberate re-check from a
	// fifteen-minute-old result would make the button look broken right after a
	// release.
	st, err := d.updater.Check(r.Context(), updater.ParseForce(r.URL.Query().Get("force")))
	if err != nil {
		data.CheckError = err.Error()
		d.render(w, "update_check", data)
		return
	}
	data.Status = &st
	data.UpdateAvailable = st.UpdateAvailable
	d.render(w, "update_check", data)
}

// postUpdate handles POST /dashboard/system/update — starts the update and
// returns the status fragment.
func (d *Dashboard) postUpdate(w http.ResponseWriter, r *http.Request) {
	if !d.requireAdmin(w, r) {
		return
	}
	data := d.newSystemData(r)

	if d.updater == nil {
		data.StartError = data.UnavailableReason
		d.render(w, "update_status", data)
		return
	}
	if err := d.updater.Start(r.Context()); err != nil {
		// A refused start — unsupported deployment, or an update already in
		// flight — is a normal answer for this button, not an HTTP failure: the
		// fragment has to reach the page either way to explain itself.
		data.StartError = err.Error()
		d.render(w, "update_status", data)
		return
	}
	// Start seeds a running state on disk; re-reading it means the fragment
	// comes back already polling instead of showing a stale idle.
	if st, err := d.updater.State(); err == nil {
		data.Run = st
	}
	d.render(w, "update_status", data)
}

// getUpdateStatus handles GET /dashboard/system/status — the fragment the
// running update polls itself with.
func (d *Dashboard) getUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !d.requireAdmin(w, r) {
		return
	}
	d.render(w, "update_status", d.newSystemData(r))
}

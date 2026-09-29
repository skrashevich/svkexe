package dashboard

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/integrations"
)

type integrationRow struct {
	Descriptor integrations.Descriptor
	Connected  bool
	Config     map[string]string
}

func (d *Dashboard) getIntegrations(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	svc := d.serviceIntegrations()
	connections, err := svc.List(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "integration operation failed", 500)
		return
	}
	rows := []integrationRow{}
	for _, desc := range svc.Descriptors() {
		row := integrationRow{Descriptor: desc}
		for _, c := range connections {
			if c.Provider == desc.ID {
				row.Connected = true
				row.Config = c.Config
			}
		}
		rows = append(rows, row)
	}
	w.Header().Set("Cache-Control", "no-store")
	d.renderPage(w, "integrations.html", struct {
		templateData
		Integrations []integrationRow
	}{d.newData(r), rows})
}
func (d *Dashboard) saveIntegration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	svc := d.serviceIntegrations()
	provider := chi.URLParam(r, "provider")
	desc, err := svc.Descriptor(provider)
	if err != nil {
		http.Error(w, "unknown provider", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	in := integrations.Input{Config: map[string]string{}, Secrets: map[string]string{}}
	for _, f := range desc.Config {
		in.Config[f.Name] = r.PostForm.Get("config_" + f.Name)
	}
	for _, f := range desc.Secrets {
		in.Secrets[f.Name] = r.PostForm.Get("secret_" + f.Name)
	}
	if err = svc.Save(r.Context(), user.ID, provider, in); err != nil {
		status := http.StatusInternalServerError
		message := "integration operation failed"
		if errors.Is(err, integrations.ErrInvalid) {
			status = http.StatusBadRequest
			message = "could not save integration; check required fields"
		}
		http.Error(w, message, status)
		return
	}
	http.Redirect(w, r, "/dashboard/integrations", http.StatusSeeOther)
}
func (d *Dashboard) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if err := d.serviceIntegrations().Delete(r.Context(), user.ID, chi.URLParam(r, "provider")); err != nil {
		http.Error(w, "integration operation failed", 500)
		return
	}
	http.Redirect(w, r, "/dashboard/integrations", http.StatusSeeOther)
}

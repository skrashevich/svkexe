package dashboard

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
)

// keyRowData is passed to the key_row template.
type keyRowData struct {
	Provider  string
	BaseURL   string
	Models    string
	Masked    string
	CreatedAt time.Time
}

// keyPageData extends templateData with Keys slice.
type keyPageData struct {
	templateData
	Keys []keyRowData
}

// getKeys handles GET /dashboard/keys — full page render.
func (d *Dashboard) getKeys(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := d.loadKeyRows(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := keyPageData{
		templateData: d.newData(r),
		Keys:         rows,
	}
	d.renderPage(w, "keys.html", data)
}

// putKey handles PUT /dashboard/keys/{provider} — upsert a key.
func (d *Dashboard) putKey(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	provider := strings.ToLower(chi.URLParam(r, "provider"))

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	// When provider is "new" the actual provider comes from the form field.
	if provider == "" || provider == "new" {
		provider = strings.ToLower(r.FormValue("provider"))
	}

	if provider == "custom" {
		provider = "custom-" + strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	}
	plaintext := r.FormValue("key")
	baseURL, models, err := db.NormalizeProvider(provider, r.FormValue("base_url"), r.FormValue("models"), plaintext)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.db.SaveProviderKey(uuid.New().String(), user.ID, provider, plaintext, baseURL, models, d.encKey); err != nil {
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	if err := d.refreshProviderKeys(r, user.ID); err != nil {
		http.Error(w, "Settings saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
		return
	}

	d.getKeyList(w, r)
}

// deleteKey handles DELETE /dashboard/keys/{provider}.
func (d *Dashboard) deleteKey(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	provider := strings.ToLower(chi.URLParam(r, "provider"))
	if !db.ValidProvider(provider) {
		http.Error(w, "invalid provider", http.StatusBadRequest)
		return
	}

	keys, err := d.db.ListAPIKeysByOwner(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	deleted := false
	for _, k := range keys {
		if strings.ToLower(k.Provider) == provider {
			if err := d.db.DeleteAPIKey(k.ID); err != nil {
				http.Error(w, "failed to delete key", http.StatusInternalServerError)
				return
			}
			deleted = true
		}
	}

	if !deleted {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := d.refreshProviderKeys(r, user.ID); err != nil {
		http.Error(w, "Settings deleted, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
		return
	}
	d.getKeyList(w, r)
}

// loadKeyRows returns masked key display rows for the given owner.
func (d *Dashboard) loadKeyRows(ownerID string) ([]keyRowData, error) {
	apiKeys, err := d.db.ListAPIKeysByOwner(ownerID)
	if err != nil {
		return nil, err
	}

	var rows []keyRowData
	for _, k := range apiKeys {
		plain, err := d.db.GetAPIKeyPlaintext(k.ID, d.encKey)
		masked := "••••••••"
		if err == nil {
			masked = maskKeyValue(plain)
		}
		rows = append(rows, keyRowData{
			Provider:  k.Provider,
			BaseURL:   k.BaseURL,
			Models:    k.Models,
			Masked:    masked,
			CreatedAt: k.CreatedAt,
		})
	}
	return rows, nil
}

// maskKeyValue masks an API key showing first 4 and last 4 chars.
func maskKeyValue(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("•", len(key))
	}
	return key[:4] + strings.Repeat("•", len(key)-8) + key[len(key)-4:]
}

func (d *Dashboard) getKeyList(w http.ResponseWriter, r *http.Request) {
	rows, err := d.loadKeyRows(userFromCtx(r.Context()).ID)
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	d.render(w, "key_list", rows)
}

func (d *Dashboard) refreshProviderKeys(r *http.Request, owner string) error {
	if d.materializer == nil || d.runtime == nil {
		return nil
	}
	containers, err := d.db.ListContainersByOwner(owner)
	if err != nil {
		return err
	}
	var syncErr error
	for _, c := range containers {
		if c.Status == "running" {
			syncErr = errors.Join(syncErr, picoclaw.RefreshProviderKeys(r.Context(), d.runtime, d.materializer, c.ID, c.IncusName, owner))
		}
	}
	return syncErr
}

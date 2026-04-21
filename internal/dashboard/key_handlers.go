package dashboard

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var validProviders = map[string]bool{
	"anthropic": true,
	"openai":    true,
	"gemini":    true,
	"fireworks": true,
}

// keyRowData is passed to the key_row template.
type keyRowData struct {
	Provider  string
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

	if !validProviders[provider] {
		http.Error(w, "invalid provider", http.StatusBadRequest)
		return
	}

	plaintext := r.FormValue("key")
	if plaintext == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	// Delete existing key for this provider+owner before inserting.
	existingKeys, err := d.db.ListAPIKeysByOwner(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, k := range existingKeys {
		if strings.ToLower(k.Provider) == provider {
			_ = d.db.DeleteAPIKey(k.ID)
		}
	}

	id := uuid.New().String()
	if err := d.db.CreateAPIKey(id, user.ID, provider, plaintext, d.encKey); err != nil {
		http.Error(w, "failed to save key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	row := keyRowData{
		Provider:  provider,
		Masked:    maskKeyValue(plaintext),
		CreatedAt: time.Now(),
	}
	d.render(w, "key_row", row)
}

// deleteKey handles DELETE /dashboard/keys/{provider}.
func (d *Dashboard) deleteKey(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	provider := strings.ToLower(chi.URLParam(r, "provider"))
	if !validProviders[provider] {
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

	w.WriteHeader(http.StatusOK)
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


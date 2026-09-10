package dashboard

import (
	"database/sql"
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
	Protocol  string
	Masked    string
	CreatedAt time.Time
}

// modelChoice is one model the owner may put their VMs on.
type modelChoice struct {
	ID       string
	Label    string
	Selected bool
}

// llmBodyData is the part of the LLM page that every change replaces. Saving a
// connection and choosing a default both rewrite the connection list and the
// choices, so the two are swapped as one fragment rather than separately: a
// half-updated page would offer a model that no longer exists.
type llmBodyData struct {
	Keys   []keyRowData
	Models []modelChoice
	// Auto reports that no explicit choice is stored, so the gateway picks.
	Auto bool
}

// keyPageData extends templateData with the LLM settings body.
type keyPageData struct {
	templateData
	llmBodyData
}

// getKeys handles GET /dashboard/keys — full page render.
func (d *Dashboard) getKeys(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := d.loadLLMBody(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	d.renderPage(w, "keys.html", keyPageData{templateData: d.newData(r), llmBodyData: body})
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
	baseURL, models, protocol, err := db.NormalizeProvider(provider, r.FormValue("base_url"), r.FormValue("models"), plaintext, r.FormValue("protocol"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.db.SaveProviderKey(uuid.New().String(), user.ID, provider, plaintext, baseURL, models, protocol, d.encKey); err != nil {
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	if err := d.refreshProviderKeys(r, user.ID); err != nil {
		http.Error(w, "Settings saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
		return
	}

	d.getLLMBody(w, r)
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
	d.getLLMBody(w, r)
}

// postDefaultModel handles POST /dashboard/keys/default — choose the model this
// owner's VMs open on. The choice is stored on the account rather than on a VM,
// which is what makes it reach the ones they create later too.
func (d *Dashboard) postDefaultModel(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// Only an unreachable model is the caller's mistake. Anything else is ours,
	// and reporting it as a validation message would render a driver error into
	// the page as if the owner had chosen badly.
	changed, err := d.db.SetUserDefaultModel(user.ID, r.FormValue("model"))
	switch {
	case errors.Is(err, db.ErrUnknownModel):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	case err != nil:
		http.Error(w, "failed to save the default model", http.StatusInternalServerError)
		return
	}
	// Syncing restarts the agent on every running VM, killing whatever it is in
	// the middle of. Re-submitting the model already stored is not worth that.
	if changed {
		if err := d.refreshProviderKeys(r, user.ID); err != nil {
			http.Error(w, "Default model saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
			return
		}
	}
	d.getLLMBody(w, r)
}

// loadLLMBody assembles the owner's connections and the models they may pick
// from. Both come from one read so the list and the choices cannot disagree.
func (d *Dashboard) loadLLMBody(ownerID string) (llmBodyData, error) {
	apiKeys, err := d.db.ListAPIKeysByOwner(ownerID)
	if err != nil {
		return llmBodyData{}, err
	}
	chosen, err := d.db.UserDefaultModel(ownerID)
	if err != nil {
		return llmBodyData{}, err
	}

	body := llmBodyData{Auto: chosen == ""}
	for _, k := range apiKeys {
		plain, err := d.db.GetAPIKeyPlaintext(k.ID, d.encKey)
		masked := "••••••••"
		if err == nil {
			masked = maskKeyValue(plain)
		}
		body.Keys = append(body.Keys, keyRowData{
			Provider:  k.Provider,
			BaseURL:   k.BaseURL,
			Models:    k.Models,
			Protocol:  k.Protocol,
			Masked:    masked,
			CreatedAt: k.CreatedAt,
		})
		if k.BaseURL == "" {
			// A provider-native key reaches the agent as an environment
			// credential, not as a model row, so there is nothing to pick.
			continue
		}
		for _, model := range strings.Split(k.Models, ",") {
			if model == "" {
				continue
			}
			id := db.UserModelID(k.Provider, model)
			body.Models = append(body.Models, modelChoice{
				ID:       id,
				Label:    k.Provider + " / " + model,
				Selected: id == chosen,
			})
		}
	}
	return body, nil
}

// maskKeyValue masks an API key showing first 4 and last 4 chars.
func maskKeyValue(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("•", len(key))
	}
	return key[:4] + strings.Repeat("•", len(key)-8) + key[len(key)-4:]
}

func (d *Dashboard) getLLMBody(w http.ResponseWriter, r *http.Request) {
	body, err := d.loadLLMBody(userFromCtx(r.Context()).ID)
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	d.render(w, "llm_body", body)
}

func (d *Dashboard) refreshProviderKeys(r *http.Request, owner string) error {
	if d.materializer == nil || d.runtime == nil {
		return nil
	}
	containers, err := d.db.ListContainersByOwner(owner)
	if err != nil {
		return err
	}
	chosen, err := d.db.UserDefaultModel(owner)
	if err != nil {
		return err
	}
	var syncErr error
	for _, c := range containers {
		if c.Status == "running" {
			syncErr = errors.Join(syncErr, picoclaw.RefreshProviderKeys(r.Context(), d.runtime, d.materializer, c.ID, c.IncusName, owner, chosen))
		}
	}
	return syncErr
}

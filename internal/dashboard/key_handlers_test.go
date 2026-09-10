package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/secrets"
)

func TestProviderSettingsLifecycle(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	enc := []byte("01234567890123456789012345678901")
	d, err := NewDashboard(database, nil, nil, "test", enc, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, owner)))
		})
	})
	d.RegisterRoutes(router)
	call := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	form := url.Values{"provider": {"openrouter"}, "key": {"secret-original"}, "models": {"vendor/model"}}
	for _, key := range []string{"secret-original", "secret-rotated"} {
		form.Set("key", key)
		rec := call("PUT", "/keys/new", form)
		if rec.Code != 200 || strings.Count(rec.Body.String(), `id="key-row-openrouter"`) != 1 || strings.Contains(rec.Body.String(), key) {
			t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rows, _ := database.ListAPIKeysByOwner(owner.ID)
	if len(rows) != 1 || rows[0].BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("rows=%+v", rows)
	}
	form.Set("models", "")
	if rec := call("PUT", "/keys/new", form); rec.Code != 400 {
		t.Fatalf("missing models: %d", rec.Code)
	}
	plain, _ := database.GetAPIKeyPlaintext(rows[0].ID, enc)
	if plain != "secret-rotated" {
		t.Fatalf("failed update lost key")
	}
	for _, name := range []string{"local", "other"} {
		rec := call("PUT", "/keys/new", url.Values{"provider": {"custom"}, "name": {name}, "base_url": {"http://localhost:8000/v1/"}, "models": {"local/model"}})
		if rec.Code != 200 {
			t.Fatalf("custom: %s", rec.Body.String())
		}
	}
	m := secrets.NewMaterializer(database, enc, t.TempDir())
	models, err := m.ProviderModels(owner.ID)
	if err != nil || len(models) != 3 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	other, _ := m.ProviderModels("other-owner")
	if len(other) != 0 {
		t.Fatal("owner leak")
	}
	if rec := call("GET", "/keys", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Custom OpenAI-compatible") {
		t.Fatalf("render: %s", rec.Body.String())
	}
	if rec := call("DELETE", "/keys/custom-local", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), `id="key-row-custom-local"`) {
		t.Fatalf("delete: %s", rec.Body.String())
	}
}

package dashboard

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/integrations"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type labProvider struct{}

func (labProvider) Descriptor() integrations.Descriptor {
	return integrations.Descriptor{ID: "lab", Name: "Lab", Config: []integrations.Field{{Name: "account", Label: "Account", Required: true}}, Secrets: []integrations.Field{{Name: "key", Label: "Key", Required: true}, {Name: "password", Label: "Password", Required: true}}, Credentials: []string{"key", "password"}}
}
func (labProvider) Validate(integrations.Input) error { return nil }
func (labProvider) Credential(in integrations.Input, n string) (string, error) {
	return in.Secrets[n], nil
}
func TestIntegrationDashboardUsesProviderDescriptors(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@test")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDashboard(database, nil, nil, "test", []byte("01234567890123456789012345678901"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.integrationService = integrations.New(database, d.encKey, labProvider{})
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, owner)))
		})
	})
	d.RegisterRoutes(router)
	call := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/integrations", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `name="secret_password"`) || !strings.Contains(w.Body.String(), `name="config_account"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/integrations/lab", url.Values{"config_account": {"team"}, "secret_key": {"private-key"}, "secret_password": {"private-password"}}); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/integrations", nil); !strings.Contains(w.Body.String(), "Connected") || strings.Contains(w.Body.String(), "private-") {
		t.Fatal("saved credentials exposed")
	}
	if w := call("POST", "/integrations/lab/delete", nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
	rows, err := d.integrationService.List(t.Context(), owner.ID)
	if err != nil || len(rows) != 0 {
		t.Fatal("disconnect failed")
	}
}

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
)

// The LLM section is where an owner points every VM they have at their own
// model, so the page has to offer that choice and the choice has to stick.
func TestLLMSectionChoosesTheDefaultModel(t *testing.T) {
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

	// api.openmodel.ai serves /responses, not /chat/completions, so the
	// protocol travels with the connection rather than being assumed.
	rec := call("PUT", "/keys/new", url.Values{
		"provider": {"custom"}, "name": {"openmodel"}, "key": {"om-XXSECRETXX-1234"},
		"base_url": {"https://api.openmodel.ai/v1"}, "protocol": {"openai-responses"},
		"models": {"deepseek-v4-flash,deepseek-v4-pro"},
	})
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	keys, err := database.ListAPIKeysByOwner(owner.ID)
	if err != nil || len(keys) != 1 || keys[0].Protocol != "openai-responses" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}

	// Saving a connection re-renders the section, which now has to offer both
	// of its models as choices.
	body := rec.Body.String()
	pro := db.UserModelID("custom-openmodel", "deepseek-v4-pro")
	for _, want := range []string{db.UserModelID("custom-openmodel", "deepseek-v4-flash"), pro, "openai-responses"} {
		if !strings.Contains(body, want) {
			t.Fatalf("section does not offer %q: %s", want, body)
		}
	}
	// The mask keeps the first and last four characters, so asserting the whole
	// key is absent would pass with almost all of it on the page. Assert the
	// interior instead — that is the part masking is supposed to hide.
	if strings.Contains(body, "SECRET") {
		t.Fatalf("key interior rendered into the page: %s", body)
	}

	if rec := call("POST", "/keys/default", url.Values{"model": {pro}}); rec.Code != 200 {
		t.Fatalf("choose: %d %s", rec.Code, rec.Body.String())
	}
	if got, err := database.UserDefaultModel(owner.ID); err != nil || got != pro {
		t.Fatalf("default=%q err=%v", got, err)
	}
	if rec := call("GET", "/keys", nil); !strings.Contains(rec.Body.String(), `value="`+pro+`" selected`) {
		t.Fatalf("chosen model not marked on the page: %s", rec.Body.String())
	}

	// A model this owner has no connection for is written into guest config if
	// accepted, so it is refused rather than stored.
	if rec := call("POST", "/keys/default", url.Values{"model": {db.UserModelID("custom-openmodel", "not-mine")}}); rec.Code != 400 {
		t.Fatalf("accepted an unreachable model: %d", rec.Code)
	}

	// Handing the choice back to the gateway is always allowed.
	if rec := call("POST", "/keys/default", url.Values{"model": {""}}); rec.Code != 200 {
		t.Fatalf("auto: %d %s", rec.Code, rec.Body.String())
	}
	if got, err := database.UserDefaultModel(owner.ID); err != nil || got != "" {
		t.Fatalf("default=%q err=%v", got, err)
	}

	// The row carries the stored settings so Edit can load them back. Without
	// that, rotating a key means retyping the connection, and a protocol left
	// on its default silently downgrades an endpoint that does not serve
	// chat completions.
	for _, want := range []string{
		`data-provider="custom-openmodel"`,
		`data-base-url="https://api.openmodel.ai/v1"`,
		`data-models="deepseek-v4-flash,deepseek-v4-pro"`,
		`data-protocol="openai-responses"`,
	} {
		if page := call("GET", "/keys", nil).Body.String(); !strings.Contains(page, want) {
			t.Fatalf("connection row does not carry %s", want)
		}
	}

	// Deleting the connection retires its models, so the section must stop
	// offering them rather than leaving a dead choice on screen.
	if rec := call("DELETE", "/keys/custom-openmodel", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), pro) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
}

// A provider-native key has no endpoint and therefore no protocol. The browser
// submits every field of the form, so the protocol control has to be able to
// say "none" — a control whose first option is a real protocol makes every
// Anthropic/OpenAI/Gemini/Fireworks key unsaveable.
func TestProviderNativeKeySavesWithoutAProtocol(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDashboard(database, nil, nil, "test", []byte("01234567890123456789012345678901"), nil, nil, nil)
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

	// Exactly what a browser posts for the "Anthropic" option: every field
	// present, the ones the user left alone empty.
	form := url.Values{"provider": {"anthropic"}, "key": {"sk-ant-x"}, "name": {""}, "base_url": {""}, "protocol": {""}, "models": {""}}
	req := httptest.NewRequest("PUT", "/keys/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	keys, err := database.ListAPIKeysByOwner(owner.ID)
	if err != nil || len(keys) != 1 || keys[0].Provider != "anthropic" || keys[0].Protocol != "" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}

	// And the page has to offer that "none" — otherwise the form above is not
	// something the UI can actually produce.
	page := call2(t, router, "GET", "/keys")
	if !strings.Contains(page, `<select name="protocol">`) {
		t.Fatal("no protocol control on the page")
	}
	control := page[strings.Index(page, `<select name="protocol">`):]
	control = control[:strings.Index(control, "</select>")]
	if !strings.Contains(control, `<option value="">`) {
		t.Fatalf("protocol control cannot express 'none': %s", control)
	}
}

func call2(t *testing.T, router chi.Router, method, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Body.String()
}

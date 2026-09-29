package api

import (
	"github.com/skrashevich/svkexe/internal/integrations"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationRoutesAreAuthenticatedAndWriteOnly(t *testing.T) {
	srv, database := newTestServer(t)
	srv.integrationService = integrations.New(database, testEncKey, integrations.GitHub{Client: &http.Client{Transport: githubTestTransport{}}})
	srv.router = srv.buildRouter()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(method, path, []byte(body)))
		return w
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/integrations", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated integrations")
	}
	if w = call("GET", "/api/integrations/providers", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "github") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("PUT", "/api/integrations/github", `{"secrets":{"token":"private-token"}}`); w.Code != 204 || strings.Contains(w.Body.String(), "private-token") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("GET", "/api/integrations", ""); w.Code != 200 || strings.Contains(w.Body.String(), "private-token") || !strings.Contains(w.Body.String(), "github") {
		t.Fatal(w.Code, w.Body.String())
	}
	svc := integrations.New(database, testEncKey)
	if got, err := svc.Credential(t.Context(), "user1", "github", "token"); err != nil || got != "private-token" {
		t.Fatal("credential not persisted")
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"rejected-token", 400}, {"unavailable-token", 503}} {
		if w = call("PUT", "/api/integrations/github", `{"secrets":{"token":"`+tc.token+`"}}`); w.Code != tc.status || strings.Contains(w.Body.String(), tc.token) {
			t.Fatal(w.Code, w.Body.String())
		}
		if got, err := svc.Credential(t.Context(), "user1", "github", "token"); err != nil || got != "private-token" {
			t.Fatal("failed verification replaced token")
		}
	}

	if w = call("DELETE", "/api/integrations/github", ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w = call("GET", "/api/integrations", ""); w.Body.String() != "[]\n" {
		t.Fatal(w.Body.String())
	}
}

type githubTestTransport struct{}

func (githubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	status := 200
	body := `{"id":1,"login":"tester"}`
	if r.Header.Get("Authorization") == "Bearer rejected-token" {
		status = 401
		body = `{"message":"Bad credentials"}`
	}
	if r.Header.Get("Authorization") == "Bearer unavailable-token" {
		status = 503
		body = `{}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

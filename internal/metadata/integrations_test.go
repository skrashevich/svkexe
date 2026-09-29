package metadata

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/integrations"
)

func TestIntegrationCredentialsRequireOwnIMDSToken(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	for _, id := range []string{"owner", "other"} {
		if err = database.CreateUser(&db.User{ID: id, Email: id + "@test", Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	svc := integrations.New(database, bytes.Repeat([]byte{1}, 32), integrations.GitHub{Client: &http.Client{Transport: metadataGitHubTransport{}}})
	ctx := t.Context()
	save := func(owner, token string) {
		t.Helper()
		if err := svc.Save(ctx, owner, "github", integrations.Input{Secrets: map[string]string{"token": token}}); err != nil {
			t.Fatal(err)
		}
	}
	save("owner", "owner-secret")
	save("other", "other-secret")
	other := sampleIdentity()
	other.ContainerID = "c-2"
	other.OwnerID = "other"
	srv := New(&fakeResolver{byAddr: map[string]*Identity{callerIP: sampleIdentity(), "10.100.0.6": other}}, Config{Integrations: svc})
	token := do(t, srv, "PUT", tokenPath, map[string]string{tokenTTLHeader: "60"}).Body.String()
	path := integrationPath + "/github/token"
	if listing := body(t, srv, "/latest/meta-data/svkexe/"); !strings.Contains(listing, "integrations/\n") || strings.Contains(listing, "secret") {
		t.Fatal(listing)
	}
	if listing := body(t, srv, integrationPath+"/"); listing != "github/\n" {
		t.Fatal(listing)
	}
	if listing := body(t, srv, integrationPath+"/github/"); listing != "token\n" {
		t.Fatal(listing)
	}
	for _, tok := range []string{"", "bad"} {
		w := do(t, srv, "GET", path, map[string]string{tokenHeader: tok})
		if w.Code != 401 || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential without valid token")
		}
	}
	w := do(t, srv, "GET", path, map[string]string{tokenHeader: token})
	if w.Code != 200 || w.Body.String() != "owner-secret" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = do(t, srv, "HEAD", path, map[string]string{tokenHeader: token}); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD leaked body")
	}
	for _, ip := range []string{"10.100.0.6", "10.100.0.99"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = ip + ":1234"
		r.Header.Set(tokenHeader, token)
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code == 200 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal("foreign caller obtained token")
		}
	}
	if w = do(t, srv, "GET", path, map[string]string{tokenHeader: token, "X-Forwarded-For": "anything"}); w.Code == 200 {
		t.Fatal("forwarded request")
	}
	for _, bad := range []string{path + "/", integrationPath + "/github/../token", integrationPath + "/github/nope", integrationPath + "/github/token/more"} {
		if w = do(t, srv, "GET", bad, map[string]string{tokenHeader: token}); w.Code != 404 {
			t.Fatalf("bad path %s: %d", bad, w.Code)
		}
	}
	save("owner", "replacement")
	if w = do(t, srv, "GET", path, map[string]string{tokenHeader: token}); w.Body.String() != "replacement" {
		t.Fatal("stale secret")
	}
	if err = svc.Delete(ctx, "owner", "github"); err != nil {
		t.Fatal(err)
	}
	if w = do(t, srv, "GET", path, map[string]string{tokenHeader: token}); w.Code != http.StatusNotFound {
		t.Fatal("deleted credential available")
	}
	if listing := body(t, srv, "/latest/meta-data/svkexe/"); strings.Contains(listing, "integrations/") {
		t.Fatal("deleted integration listed")
	}
}

// A provider with two credentials exercises the metadata adapter without GitHub assumptions.
type labMetadataProvider struct{}

func (labMetadataProvider) Descriptor() integrations.Descriptor {
	return integrations.Descriptor{ID: "lab", Name: "Lab", Secrets: []integrations.Field{{Name: "key", Required: true}, {Name: "password", Required: true}}, Credentials: []string{"key", "password"}}
}
func (labMetadataProvider) Validate(context.Context, integrations.Input) error { return nil }
func (labMetadataProvider) Credential(in integrations.Input, name string) (string, error) {
	return in.Secrets[name], nil
}
func TestMetadataSecondProvider(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if err = database.CreateUser(&db.User{ID: "owner", Email: "owner@test", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(database, bytes.Repeat([]byte{1}, 32), labMetadataProvider{})
	if err = svc.Save(t.Context(), "owner", "lab", integrations.Input{Secrets: map[string]string{"key": "key-secret", "password": "password-secret"}}); err != nil {
		t.Fatal(err)
	}
	srv := New(&fakeResolver{byAddr: map[string]*Identity{callerIP: sampleIdentity()}}, Config{Integrations: svc})
	if listing := body(t, srv, integrationPath+"/"); listing != "lab/\n" {
		t.Fatal(listing)
	}
	if listing := body(t, srv, integrationPath+"/lab/"); listing != "key\npassword\n" {
		t.Fatal(listing)
	}
	token := do(t, srv, "PUT", tokenPath, map[string]string{tokenTTLHeader: "60"}).Body.String()
	for _, name := range []string{"key", "password"} {
		w := do(t, srv, "GET", integrationPath+"/lab/"+name, map[string]string{tokenHeader: token})
		if w.Code != 200 || w.Body.String() != name+"-secret" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

type metadataGitHubTransport struct{}

func (metadataGitHubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":1,"login":"tester"}`)), Request: r}, nil
}

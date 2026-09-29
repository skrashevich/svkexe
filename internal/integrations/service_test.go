package integrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

type testProvider struct{}

func (testProvider) Descriptor() Descriptor {
	return Descriptor{ID: "lab", Name: "Lab", Config: []Field{{Name: "account", Required: true}}, Secrets: []Field{{Name: "key", Required: true}, {Name: "password", Required: true}}, Credentials: []string{"access", "password"}}
}
func (testProvider) Validate(context.Context, Input) error { return nil }
func (testProvider) Credential(in Input, n string) (string, error) {
	if n == "access" {
		return in.Config["account"] + ":" + in.Secrets["key"], nil
	}
	return in.Secrets[n], nil
}
func setup(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })
	for _, id := range []string{"a", "b"} {
		if err = database.CreateUser(&db.User{ID: id, Email: id + "@test", Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	return New(database, bytes.Repeat([]byte{1}, 32), GitHub{}, testProvider{})
}
func TestProviderLifecycleAndIsolation(t *testing.T) {
	s := setup(t)
	ctx := t.Context()
	in := Input{Config: map[string]string{"account": "team"}, Secrets: map[string]string{"key": "secret-key", "password": "secret-password"}}
	if err := s.Save(ctx, "a", "lab", in); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Credential(ctx, "a", "lab", "access"); err != nil || got != "team:secret-key" {
		t.Fatalf("credential %q %v", got, err)
	}
	if _, err := s.Credential(ctx, "b", "lab", "access"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, "a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list %v %v", rows, err)
	}
	encoded, _ := json.Marshal(rows)
	if bytes.Contains(encoded, []byte("secret-")) {
		t.Fatal("list leaked secrets")
	}
	var encrypted []byte
	s.db.QueryRow(`SELECT credentials FROM user_integrations WHERE owner_id='a'`).Scan(&encrypted)
	if bytes.Contains(encrypted, []byte("secret-")) {
		t.Fatal("plaintext storage")
	}
	in.Secrets["key"] = "new-key"
	if err = s.Save(ctx, "a", "lab", in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credential(ctx, "a", "lab", "access")
	if got != "team:new-key" || err != nil {
		t.Fatal("replacement failed")
	}
	if err = s.Delete(ctx, "b", "lab"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Credential(ctx, "a", "lab", "access"); err != nil {
		t.Fatal("foreign delete removed integration")
	}
	s.db.Exec(`INSERT INTO user_integrations(owner_id,provider,config,credentials) SELECT 'b',provider,config,credentials FROM user_integrations WHERE owner_id='a'`)
	if _, err = s.Credential(ctx, "b", "lab", "access"); err == nil {
		t.Fatal("transplanted ciphertext decrypted")
	}
	s.db.Exec(`DELETE FROM users WHERE id='a'`)
	if rows, err = s.List(ctx, "a"); err != nil || len(rows) != 0 {
		t.Fatal("delete did not cascade")
	}
}
func TestInvalidInputs(t *testing.T) {
	s := setup(t)
	for _, tc := range []struct {
		owner, provider string
		in              Input
	}{
		{"", "github", Input{Secrets: map[string]string{"token": "abc"}}},
		{"a", "unknown", Input{}},
		{"a", "github", Input{Config: map[string]string{"token": "secret"}, Secrets: map[string]string{"token": "abc"}}},
		{"a", "github", Input{Secrets: map[string]string{"token": "bad\nvalue"}}},
		{"a", "github", Input{}},
	} {
		if !errors.Is(s.Save(t.Context(), tc.owner, tc.provider, tc.in), ErrInvalid) {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestHTTPContractSupportsSecondProvider(t *testing.T) {
	s := setup(t)
	owner := "a"
	handler := s.HTTPHandler(func(*http.Request) string { return owner }, func(*http.Request) string { return "lab" })
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	for _, bad := range []string{`{"secrets":{},"unexpected":"secret"}`, `{} {}`, `{"config":{"account":"team"},"secrets":{"key":"secret","password":"secret"}}` + strings.Repeat(" ", 65536)} {
		if w := request("PUT", bad); w.Code != 400 {
			t.Fatalf("bad input accepted: %d", w.Code)
		}
	}
	if w := request("PUT", `{"config":{"account":"team"},"secrets":{"key":"secret","password":"hidden"}}`); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("GET", ""); w.Code != 200 || strings.Contains(w.Body.String(), "hidden") || !strings.Contains(w.Body.String(), "team") {
		t.Fatal(w.Body.String())
	}
	owner = "b"
	if w := request("GET", ""); w.Body.String() != "[]\n" {
		t.Fatal("owner isolation failed")
	}
	request("DELETE", "")
	owner = "a"
	if w := request("GET", ""); !strings.Contains(w.Body.String(), "lab") {
		t.Fatal("foreign delete")
	}
	request("DELETE", "")
	if w := request("GET", ""); w.Body.String() != "[]\n" {
		t.Fatal("delete failed")
	}
}

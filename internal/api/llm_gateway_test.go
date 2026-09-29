package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/llmproxy"
)

func TestVMGatewayOwnerModels(t *testing.T) {
	s, database := newTestServer(t)
	if err := database.CreateContainer(&db.Container{ID: "vm1", OwnerID: "user1", Name: "box", IncusName: "svkexe-user1-box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("own", "user1", "custom-test", "secret", "https://provider.test/v1", "mine", "", testEncKey); err != nil {
		t.Fatal(err)
	}
	s = NewServer(database, newMockRuntime(), testEncKey, "example.test", nil, nil, &llmproxy.Config{APIKey: "platform", InternalToken: "legacy", Models: []string{"platform-model"}}, nil, nil, nil)
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/llm/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	token := llmproxy.VMToken(testEncKey, "vm1", "user1")
	w := call(token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), db.UserModelID("custom-test", "mine")) || strings.Contains(w.Body.String(), "platform-model") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := call(token + "tamper"); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d", w.Code)
	}
	if w := call(llmproxy.VMToken(testEncKey, "vm1", "other")); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong owner: %d", w.Code)
	}
	if w := call("legacy"); w.Code != 200 || !strings.Contains(w.Body.String(), "platform-model") {
		t.Fatalf("legacy: %d %s", w.Code, w.Body.String())
	}
	s = NewServer(database, newMockRuntime(), testEncKey, "example.test", nil, nil, nil, nil, nil, nil)
	if w := call(token); w.Code != 200 {
		t.Fatalf("no platform key: %d %s", w.Code, w.Body.String())
	}
}

func TestVMGatewayReadsCurrentOwnerConnections(t *testing.T) {
	s, database := newTestServer(t)
	if err := database.CreateContainer(&db.Container{ID: "vm", OwnerID: "user1", Name: "box", IncusName: "svkexe-user1-box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(&db.User{ID: "user2", Email: "other@example.test", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveProviderKey("other", "user2", "custom-other", "other-key", "https://other.test/v1", "other-model", "", testEncKey); err != nil {
		t.Fatal(err)
	}
	var expectedKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedKey {
			t.Errorf("incorrect upstream credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()
	s = NewServer(database, newMockRuntime(), testEncKey, "example.test", nil, nil, &llmproxy.Config{APIKey: "platform", InternalToken: "legacy", Models: []string{"platform-model"}}, nil, nil, nil)
	token := llmproxy.VMToken(testEncKey, "vm", "user1")
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/llm/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, key := range []string{"first-key", "rotated-key"} {
		expectedKey = key
		if err := database.SaveProviderKey("own", "user1", "custom-own", key, upstream.URL+"/v1", "mine", "", testEncKey); err != nil {
			t.Fatal(err)
		}
		if w := call("POST", "/chat/completions", `{"model":"mine","messages":[]}`); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if w := call("GET", "/models", ""); strings.Contains(w.Body.String(), "other-model") {
			t.Fatal("another owner's model leaked")
		}
	}
	if err := database.DeleteAPIKey("own"); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/models", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "platform-model") {
		t.Fatalf("fallback: %d %s", w.Code, w.Body.String())
	}
	if err := database.DeleteContainer("vm"); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/models", ""); w.Code != 401 {
		t.Fatalf("deleted VM token: %d", w.Code)
	}
}

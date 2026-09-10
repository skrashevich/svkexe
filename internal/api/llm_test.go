package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// The REST API has to expose the same two things the dashboard does: what the
// owner can point their VMs at, and which of those they picked.
func TestLLMDefaultModelEndpoints(t *testing.T) {
	srv, database := newTestServer(t)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != "" {
			payload = []byte(body)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(method, path, payload))
		return w
	}

	if w := call(http.MethodPost, "/api/keys", `{"provider":"custom-openmodel","key":"om-secret","base_url":"https://api.openmodel.ai/v1","models":"deepseek-v4-flash,deepseek-v4-pro","protocol":"openai-responses"}`); w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	keys, err := database.ListAPIKeysByOwner("user1")
	if err != nil || len(keys) != 1 || keys[0].Protocol != "openai-responses" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}

	var listed llmModelsResponse
	w := call(http.MethodGet, "/api/llm/models", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list models: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	pro := dbpkg.UserModelID("custom-openmodel", "deepseek-v4-pro")
	want := []string{dbpkg.UserModelID("custom-openmodel", "deepseek-v4-flash"), pro}
	if len(listed.Models) != len(want) || listed.Models[0] != want[0] || listed.Models[1] != want[1] || listed.Default != "" {
		t.Fatalf("models=%+v want %v with no default", listed, want)
	}

	if w := call(http.MethodPut, "/api/llm/default", `{"model":"`+pro+`"}`); w.Code != http.StatusOK {
		t.Fatalf("set default: %d %s", w.Code, w.Body.String())
	}
	if got, err := database.UserDefaultModel("user1"); err != nil || got != pro {
		t.Fatalf("default=%q err=%v", got, err)
	}

	// A model this owner has no connection for would be written into guest
	// configuration verbatim, so it has to be refused here.
	if w := call(http.MethodPut, "/api/llm/default", `{"model":"`+dbpkg.UserModelID("custom-openmodel", "not-mine")+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("accepted an unreachable model: %d %s", w.Code, w.Body.String())
	}

	// Handing the choice back to the gateway is always allowed.
	if w := call(http.MethodPut, "/api/llm/default", `{"model":""}`); w.Code != http.StatusOK {
		t.Fatalf("auto: %d %s", w.Code, w.Body.String())
	}
	if got, err := database.UserDefaultModel("user1"); err != nil || got != "" {
		t.Fatalf("default=%q err=%v", got, err)
	}
}

// An endpoint that speaks a protocol the agent does not know leaves the model
// unusable with nothing to explain why, so it is rejected at the door.
func TestCreateKeyRejectsAnUnknownProtocol(t *testing.T) {
	srv, _ := newTestServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, authedRequest(http.MethodPost, "/api/keys",
		[]byte(`{"provider":"custom-local","key":"k","base_url":"https://host/v1","models":"m","protocol":"grpc"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

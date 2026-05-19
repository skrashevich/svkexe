package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectHandler_WithToken(t *testing.T) {
	const token = "metrics-secret"
	old := BearerToken
	BearerToken = token
	defer func() { BearerToken = old }()

	called := false
	h := ProtectHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without bearer: want 401, got %d", w.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK || !called {
		t.Fatalf("with bearer: want 200 and handler called, got %d called=%v", w2.Code, called)
	}
}

func TestProtectHandler_NoTokenConfigured(t *testing.T) {
	old := BearerToken
	BearerToken = ""
	defer func() { BearerToken = old }()

	h := ProtectHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 when token unset, got %d", w.Code)
	}
}

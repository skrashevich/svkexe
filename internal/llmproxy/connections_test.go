package llmproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/secrets"
)

func TestConnectionsNativeProtocols(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-responses", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			endpoint := map[string]string{"openai": "/chat/completions", "openai-responses": "/responses", "anthropic": "/messages", "gemini": "/models/gem-model:generateContent"}[protocol]
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expected := "/v1" + endpoint
				if r.URL.Path != expected {
					t.Errorf("path %s want %s", r.URL.Path, expected)
				}
				if protocol == "anthropic" {
					if r.Header.Get("X-Api-Key") != "owner-key" {
						t.Error("missing anthropic key")
					}
				} else if protocol == "gemini" {
					if r.Header.Get("X-Goog-Api-Key") != "owner-key" {
						t.Error("missing gemini key")
					}
				} else if r.Header.Get("Authorization") != "Bearer owner-key" {
					t.Error("wrong upstream key")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if protocol != "gemini" && body["model"] != "gem-model" {
					t.Errorf("model %v", body["model"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: ok\n\n"))
			}))
			defer upstream.Close()
			base := upstream.URL + "/v1"
			if protocol == "anthropic" {
				base += "/messages"
			}
			models := []secrets.ProviderModel{{Provider: "custom-test", Model: "gem-model", BaseURL: base, Key: "owner-key", Protocol: protocol}}
			path := endpoint
			if protocol == "gemini" {
				path = "/models/" + models[0].ID() + ":generateContent"
			}
			req := httptest.NewRequest("POST", "/api/llm/v1"+path, strings.NewReader(`{"model":"`+models[0].ID()+`","stream":true,"messages":[],"input":"hello"}`))
			req.Header.Set("Authorization", "Bearer vm-token")
			w := httptest.NewRecorder()
			ServeConnections(w, req, models, "")
			if w.Code != 200 || w.Body.String() != "data: ok\n\n" || !w.Flushed {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestConnectionsRejectUnknownOrWrongProtocol(t *testing.T) {
	models := []secrets.ProviderModel{{Provider: "custom-test", Model: "mine", BaseURL: "https://invalid.test", Key: "key", Protocol: "openai-responses"}}
	for _, model := range []string{"other", models[0].ID()} {
		w := httptest.NewRecorder()
		ServeConnections(w, httptest.NewRequest("POST", "/api/llm/v1/chat/completions", strings.NewReader(`{"model":"`+model+`"}`)), models, "")
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestConnectionsDoNotFallbackOrLeakProviderErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://other.test")
		w.WriteHeader(302)
		w.Write([]byte("secret-key"))
	}))
	defer upstream.Close()
	models := []secrets.ProviderModel{{Provider: "custom-test", Model: "mine", BaseURL: upstream.URL, Key: "secret-key", Protocol: "openai"}}
	w := httptest.NewRecorder()
	ServeConnections(w, httptest.NewRequest("POST", "/api/llm/v1/chat/completions", strings.NewReader(`{"model":"mine"}`)), models, "")
	if w.Code != 502 || strings.Contains(w.Body.String(), "secret-key") || w.Header().Get("Location") != "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

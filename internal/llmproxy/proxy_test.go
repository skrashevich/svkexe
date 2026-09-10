package llmproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAgentGatewayToolCallsAndFallback(t *testing.T) {
	p := New(Config{APIKey: "upstream-key", InternalToken: "agent-token", Models: []string{"first", "second"}})
	var attempted []string
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Error("internal token forwarded upstream")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var model string
		json.Unmarshal(body["model"], &model)
		attempted = append(attempted, model)
		for _, key := range []string{"messages", "tools", "tool_choice", "temperature"} {
			if len(body[key]) == 0 {
				t.Errorf("lost agent request field %s", key)
			}
		}
		code, text := http.StatusOK, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`
		if model == "second" {
			code, text = http.StatusTooManyRequests, `{"error":"busy"}`
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(text))}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/llm/v1/chat/completions", strings.NewReader(`{"model":"second","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"bash"}}],"tool_choice":"auto","temperature":0}`))
	req.Header.Set("Authorization", "Bearer agent-token")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"call-1"`) {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	if strings.Join(attempted, ",") != "second,first" {
		t.Fatalf("fallback order %v", attempted)
	}
}
func TestAgentGatewayAuthentication(t *testing.T) {
	p := New(Config{InternalToken: "secret", Models: []string{"model"}})
	for _, handler := range []http.HandlerFunc{p.ServeHTTP, p.ServeModels} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d", w.Code)
		}
	}
}
func TestAgentGatewayStreamingFlush(t *testing.T) {
	p := New(Config{Models: []string{"allowed"}})
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "allowed" {
			t.Errorf("unconfigured model escaped allowlist: %s", req.Model)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: delta\n\ndata: [DONE]\n\n"))}, nil
	})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"model":"unknown","stream":true,"messages":[]}`)))
	if !w.Flushed || w.Body.String() != "data: delta\n\ndata: [DONE]\n\n" {
		t.Fatal("agent stream buffered or changed")
	}
}

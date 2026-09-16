package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
)

type citationTransport func(*http.Request) (*http.Response, error)

func (f citationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the public entry points, not just the individual serializers. The
// transport is synchronous and local: invalid history must never reach HTTP.
func TestCitationAdapterBoundaries(t *testing.T) {
	const urlCitation = `{"type":"url_citation","start_index":0,"end_index":6,"url":"https://example.com/source","title":"Source title"}`
	const webCitation = `{"type":"web_search_result_location","url":"https://example.com/web","title":"Web title","cited_text":"Original passage","encrypted_index":"original-signed-index","opaque":"retain-me"}`
	for _, adapter := range []struct {
		name        string
		service     func(*http.Client) llm.Service
		response    string
		contentType string
	}{
		{
			name:        "anthropic",
			service:     func(c *http.Client) llm.Service { return &ant.Service{HTTPC: c, URL: "http://provider.test/messages"} },
			response:    "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			contentType: "text/event-stream",
		},
		{
			name: "openai-chat",
			service: func(c *http.Client) llm.Service {
				return &oai.Service{HTTPC: c, Model: oai.GPT41, ModelURL: "http://provider.test"}
			},
			response:    `{"id":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			contentType: "application/json",
		},
		{
			name: "openai-responses",
			service: func(c *http.Client) llm.Service {
				return &oai.ResponsesService{HTTPC: c, Model: oai.GPT41, ModelURL: "http://provider.test"}
			},
			response:    `{"id":"test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
			contentType: "application/json",
		},
		{
			name: "openai-responses/fireworks",
			service: func(c *http.Client) llm.Service {
				return &oai.ResponsesService{HTTPC: c, Model: oai.GPT41, ModelURL: "http://provider.test", ProviderName: "fireworks"}
			},
			response:    `{"id":"test","status":"completed","output":[]}`,
			contentType: "application/json",
		},
		{
			name: "openai-responses/xai",
			service: func(c *http.Client) llm.Service {
				return &oai.ResponsesService{HTTPC: c, Model: oai.GPT41, ModelURL: "http://provider.test", ProviderName: "xai"}
			},
			response:    `{"id":"test","status":"completed","output":[]}`,
			contentType: "application/json",
		},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			var payloads [][]byte
			client := &http.Client{Transport: citationTransport(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				payloads = append(payloads, body)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{adapter.contentType}}, Body: io.NopCloser(strings.NewReader(adapter.response)), Request: r}, nil
			})}
			svc := adapter.service(client)
			makeRequest := func(raw string) *llm.Request {
				return &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Answer", Citations: json.RawMessage(raw)}}}}}
			}
			for _, raw := range []string{`[{"type":"future_citation"}]`, `[{`, "[" + urlCitation + `,{"type":"future"}]`, `[{"type":"url_citation","url":42}]`} {
				req := makeRequest(raw)
				if _, err := svc.Do(context.Background(), req); err == nil || !strings.Contains(err.Error(), strings.Split(adapter.name, "/")[0]) || !strings.Contains(err.Error(), "messages[0].content[0].citations") {
					t.Fatalf("invalid citation error = %v", err)
				}
				if len(payloads) != 0 {
					t.Fatal("invalid citation reached HTTP")
				}
				if string(req.Messages[0].Content[0].Citations) != raw || req.Messages[0].Content[0].Text != "Answer" {
					t.Fatal("invalid history was mutated")
				}
			}
			req := makeRequest("[" + urlCitation + "," + webCitation + "]")
			// Tool results must be prepared before either serializer flattens them.
			req.Messages[0].Content = append(req.Messages[0].Content, llm.Content{Type: llm.ContentTypeToolUse, ID: "call", ToolName: "lookup", ToolInput: json.RawMessage(`{}`)})
			req.Messages = append(req.Messages, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "call", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "Nested answer", Citations: json.RawMessage("[" + urlCitation + "]")}}}}})
			// Top-level user text cannot carry output_text annotations either.
			req.Messages = append(req.Messages, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "User answer", Citations: json.RawMessage("[" + urlCitation + "]")}}})
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := svc.Do(context.Background(), req); err != nil {
					t.Fatal(err)
				}
			}
			if len(payloads) != 2 || !bytes.Equal(payloads[0], payloads[1]) {
				t.Fatal("repeated requests differed")
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("adapter mutated original history")
			}
			body := string(payloads[0])
			for _, want := range []string{"Source title", "https://example.com/source", "Web title", "https://example.com/web", "Original passage", `Nested answer\n\nSource: Source title`, `User answer\n\nSource: Source title`} {
				if !strings.Contains(body, want) {
					t.Fatalf("outgoing request lost %q: %s", want, body)
				}
			}
			if adapter.name == "openai-responses" {
				if strings.Count(body, `"url_citation"`) != 1 || !strings.Contains(body, `"annotations":[`+urlCitation+`]`) {
					t.Fatalf("native assistant annotation not preserved exclusively: %s", body)
				}
				if !strings.Contains(body, `"text":"Answer\n\nSource: Web title`) {
					t.Fatalf("native citation changed text or foreign citation was not converted: %s", body)
				}
			} else if strings.Contains(body, `"url_citation"`) || strings.Contains(body, `"annotations"`) {
				t.Fatalf("foreign OpenAI citation metadata survived: %s", body)
			}
			if adapter.name == "anthropic" {
				if !strings.Contains(body, `"citations":[`+webCitation+`]`) {
					t.Fatalf("native signed Anthropic object was not preserved: %s", body)
				}
			} else {
				for _, absent := range []string{`"citations"`, `"web_search_result_location"`, "original-signed-index", "retain-me"} {
					if strings.Contains(body, absent) {
						t.Fatalf("outgoing request leaked citation metadata %q: %s", absent, body)
					}
				}
				if !strings.Contains(body, `Cited text: Original passage`) {
					t.Fatalf("reverse conversion lost cited text: %s", body)
				}
			}
		})
	}
}

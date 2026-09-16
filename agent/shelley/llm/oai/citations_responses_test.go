package oai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
)

type citationRoundTripper func(*http.Request) (*http.Response, error)

func (f citationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func citationHTTPResponse(r *http.Request, contentType, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

// Start at the actual wire decoder, persist through the application's database,
// and replay via Do. Typed intermediate fixtures would conceal ingest data loss.
func TestResponsesNativeCitationsPersistAndReplay(t *testing.T) {
	const parts = `[{"type":"output_text","text":"Résumé citeturn0search0","annotations":[{"type":"url_citation","start_index":0,"end_index":6,"url":"https://example.com/first","title":"","extra":{"zero":0,"empty":"","nullable":null,"integer":9007199254740993}},{"type":"url_citation","start_index":7,"end_index":26,"url":"https://example.com/second","title":"Second","opaque":[false,0,""]}]},{"type":"output_text","text":"Next part","annotations":[{"type":"url_citation","start_index":0,"end_index":4,"url":"https://example.com/third","title":"Third","extra":false}]}]`
	const item = `{"id":"msg_native","type":"message","role":"assistant","content":` + parts + `}`
	const response = `{"id":"resp_native","status":"completed","output":[` + item + `]}`
	for _, encoding := range []string{"json", "sse"} {
		for _, provider := range []string{"", "openai"} {
			t.Run(encoding+"/"+provider, func(t *testing.T) {
				ctx := context.Background()
				var logs bytes.Buffer
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previousLogger) })
				var payloads [][]byte
				client := &http.Client{Transport: citationRoundTripper(func(r *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					payloads = append(payloads, body)
					if len(payloads) != 1 {
						return citationHTTPResponse(r, "application/json", `{"id":"ok","status":"completed","output":[]}`), nil
					}
					if encoding == "sse" {
						stream := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
							"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native\",\"status\":\"completed\",\"output\":[]}}\n\n"
						return citationHTTPResponse(r, "text/event-stream", stream), nil
					}
					return citationHTTPResponse(r, "application/json", response), nil
				})}
				svc := &oai.ResponsesService{HTTPC: client, Model: oai.GPT41, ProviderName: provider, ModelURL: "http://provider.test"}
				got, err := svc.Do(ctx, &llm.Request{Messages: []llm.Message{llm.UserStringMessage("Research")}})
				if err != nil {
					t.Fatal(err)
				}
				database, cleanup := db.NewTestDB(t)
				defer cleanup()
				conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.CreateMessage(ctx, db.CreateMessageParams{ConversationID: conversation.ConversationID, Type: db.MessageTypeAgent, LLMData: got.ToMessage()}); err != nil {
					t.Fatal(err)
				}
				stored, err := database.ListMessagesForContext(ctx, conversation.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				if len(stored) != 1 || stored[0].LlmData == nil {
					t.Fatalf("stored messages = %+v", stored)
				}
				var reloaded llm.Message
				if err := json.Unmarshal([]byte(*stored[0].LlmData), &reloaded); err != nil {
					t.Fatal(err)
				}
				before, err := json.Marshal(reloaded)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if _, err := svc.Do(ctx, &llm.Request{Messages: []llm.Message{reloaded}}); err != nil {
						t.Fatal(err)
					}
				}
				if len(payloads) != 3 || !bytes.Equal(payloads[1], payloads[2]) {
					t.Fatal("repeated native replay differed")
				}
				var replay struct {
					Input []struct {
						Type    string          `json:"type"`
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"input"`
				}
				if err := json.Unmarshal(payloads[1], &replay); err != nil {
					t.Fatal(err)
				}
				if len(replay.Input) != 1 || replay.Input[0].Type != "message" || replay.Input[0].Role != "assistant" {
					t.Fatalf("replay = %s", payloads[1])
				}
				var want, actual bytes.Buffer
				if err := json.Compact(&want, []byte(parts)); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&actual, replay.Input[0].Content); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want.Bytes(), actual.Bytes()) {
					t.Fatalf("text/annotation parts changed:\n got %s\nwant %s", actual.Bytes(), want.Bytes())
				}
				if bytes.Contains(payloads[1], []byte("Source:")) || strings.Contains(logs.String(), "converted request citations") {
					t.Fatalf("native citations converted: payload=%s logs=%s", payloads[1], logs.String())
				}
				after, err := json.Marshal(reloaded)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("replaying mutated persisted native message")
				}
			})
		}
	}
}

func TestResponsesNativeCitationInvalidReplayNeverCallsHTTP(t *testing.T) {
	const valid = `{"type":"url_citation","url":"https://example.com","title":"","start_index":0,"end_index":0}`
	for _, tt := range []struct{ name, citation, text, reason string }{
		{"unknown", `{"type":"future_annotation","extra":"keep"}`, "answer", "unsupported citation type"},
		{"not object", `42`, "answer", "expected citation object"},
		{"URL shape", strings.Replace(valid, `"https://example.com"`, `42`, 1), "answer", "url must"},
		{"title shape", strings.Replace(valid, `"title":""`, `"title":null`, 1), "answer", "title must"},
		{"index shape", strings.Replace(valid, `"start_index":0`, `"start_index":false`, 1), "answer", "start_index must"},
		{"negative index", strings.Replace(valid, `"end_index":0`, `"end_index":-1`, 1), "answer", "end_index must"},
		{"legacy missing start", strings.Replace(valid, `"start_index":0,`, ``, 1), "answer", "missing annotation fields require explicit repair; do not synthesize them on replay"},
		{"legacy missing end", strings.Replace(valid, `,"end_index":0`, ``, 1), "answer", "missing annotation fields require explicit repair; do not synthesize them on replay"},
		{"legacy missing title", strings.Replace(valid, `"title":"",`, ``, 1), "answer", "missing annotation fields require explicit repair; do not synthesize them on replay"},
		{"empty cited text", valid, "", "empty assistant text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			svc := &oai.ResponsesService{Model: oai.GPT41, HTTPC: &http.Client{Transport: citationRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				// Ingest even unknown/malformed annotations losslessly; replay
				// owns validation rather than silently dropping unknown fields.
				text, err := json.Marshal(tt.text)
				if err != nil {
					t.Fatal(err)
				}
				return citationHTTPResponse(r, "application/json", `{"id":"test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+string(text)+`,"annotations":[`+tt.citation+`]}]}]}`), nil
			})}}
			got, err := svc.Do(context.Background(), &llm.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Content) != 1 || string(got.Content[0].Citations) != "["+tt.citation+"]" {
				t.Fatalf("ingest discarded annotations: %+v", got.Content)
			}
			if _, err := svc.Do(context.Background(), &llm.Request{Messages: []llm.Message{got.ToMessage()}}); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error = %v, want %q", err, tt.reason)
			}
			if calls != 1 {
				t.Fatal("invalid replay reached HTTP")
			}
		})
	}
}

func TestResponsesCitationPositions(t *testing.T) {
	const annotation = `[{"type":"url_citation","url":"https://example.com","title":"Title","start_index":0,"end_index":6}]`
	for _, tt := range []struct {
		name   string
		role   llm.MessageRole
		nested bool
		retain bool
	}{
		{"assistant", llm.MessageRoleAssistant, false, true},
		{"user", llm.MessageRoleUser, false, false},
		{"user tool result", llm.MessageRoleUser, true, false},
		{"assistant tool result", llm.MessageRoleAssistant, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var payload []byte
			svc := &oai.ResponsesService{Model: oai.GPT41, HTTPC: &http.Client{Transport: citationRoundTripper(func(r *http.Request) (*http.Response, error) {
				var err error
				payload, err = io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				return citationHTTPResponse(r, "application/json", `{"id":"ok","status":"completed","output":[]}`), nil
			})}}
			content := llm.Content{Type: llm.ContentTypeText, Text: "Answer", Citations: json.RawMessage(annotation)}
			if tt.nested {
				content = llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "call", ToolResult: []llm.Content{content}}
			}
			if _, err := svc.Do(context.Background(), &llm.Request{Messages: []llm.Message{{Role: tt.role, Content: []llm.Content{content}}}}); err != nil {
				t.Fatal(err)
			}
			if tt.retain {
				if !bytes.Contains(payload, []byte(`"type":"output_text","text":"Answer","annotations":`+annotation)) || bytes.Contains(payload, []byte("Source:")) {
					t.Fatalf("native annotation converted/lost: %s", payload)
				}
			} else if bytes.Contains(payload, []byte(`"annotations"`)) || !bytes.Contains(payload, []byte(`Answer\n\nSource: Title — https://example.com`)) {
				t.Fatalf("citation outside assistant text was not converted: %s", payload)
			}
		})
	}
}

func TestResponsesNoCitationHistoryUnchanged(t *testing.T) {
	var payloads [][]byte
	svc := &oai.ResponsesService{Model: oai.GPT41, HTTPC: &http.Client{Transport: citationRoundTripper(func(r *http.Request) (*http.Response, error) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
		return citationHTTPResponse(r, "application/json", `{"id":"ok","status":"completed","output":[]}`), nil
	})}}
	for _, raw := range []string{"", "null", "[]"} {
		for _, role := range []llm.MessageRole{llm.MessageRoleUser, llm.MessageRoleAssistant} {
			message := llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Ordinary text", Citations: json.RawMessage(raw)}}}
			if _, err := svc.Do(context.Background(), &llm.Request{Messages: []llm.Message{message}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, payload := range payloads {
		if !bytes.Equal(payload, payloads[i%2]) || bytes.Contains(payload, []byte(`"annotations"`)) {
			t.Fatalf("absent citation changed ordinary history: %s", payload)
		}
	}
}

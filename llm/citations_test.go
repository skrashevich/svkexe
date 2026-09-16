package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const (
	convertedCitation = `{"type":"convert","private":"example.com Source title"}`
	retainedCitation  = `{"type":"retain","opaque":{"keep":true}}`
)

func fakeAdaptCitation(_ CitationContext, kind string, fields map[string]json.RawMessage) (string, bool, error) {
	switch kind {
	case "retain":
		return "", true, nil
	case "convert":
		return "\n\nSource: Source title — https://example.com/source", false, nil
	case "empty":
		return "", false, nil
	default:
		return "", false, fmt.Errorf("unsupported citation type %q", kind)
	}
}

func citationRequest(raw string) *Request {
	return &Request{Messages: []Message{{Role: MessageRoleAssistant, Content: []Content{{Type: ContentTypeText, Text: "Answer", Citations: json.RawMessage(raw)}}}}}
}

func TestPrepareRequestCitations(t *testing.T) {
	native := " [ \n" + retainedCitation + " ] "
	for _, tt := range []struct {
		name     string
		raw      string
		wantRaw  string
		wantText string
	}{
		{"converted", "[" + convertedCitation + "]", "", "Answer\n\nSource: Source title — https://example.com/source"},
		{"retained byte preservation", native, native, "Answer"},
		{"mixed", "[" + retainedCitation + "," + convertedCitation + "]", "[" + retainedCitation + "]", "Answer\n\nSource: Source title — https://example.com/source"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := citationRequest(tt.raw)
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation)
			if err != nil {
				t.Fatal(err)
			}
			c := got.Messages[0].Content[0]
			if c.Text != tt.wantText || string(c.Citations) != tt.wantRaw {
				t.Fatalf("got text %q citations %s; want text %q citations %s", c.Text, c.Citations, tt.wantText, tt.wantRaw)
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || string(req.Messages[0].Content[0].Citations) != tt.raw {
				t.Fatal("preparation mutated original history")
			}
			for _, input := range []*Request{req, got} {
				again, err := PrepareRequestCitations(context.Background(), input, "test-adapter", fakeAdaptCitation)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, again) {
					t.Fatal("preparation is not repeatable/idempotent")
				}
			}
			var reloaded Request
			if err := json.Unmarshal(before, &reloaded); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := PrepareRequestCitations(context.Background(), &reloaded, "test-adapter", fakeAdaptCitation)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			roundTripJSON, _ := json.Marshal(roundTrip)
			if !bytes.Equal(gotJSON, roundTripJSON) {
				t.Fatal("JSON persistence changed preparation")
			}
			// Even the retained raw bytes must not alias the caller's history.
			got.Messages[0].Role = MessageRoleUser
			got.Messages[0].Content[0].Text = "changed"
			if len(c.Citations) > 0 {
				c.Citations[0] = '!'
			}
			if req.Messages[0].Role != MessageRoleAssistant || req.Messages[0].Content[0].Text != "Answer" || string(req.Messages[0].Content[0].Citations) != tt.raw {
				t.Fatal("prepared history aliases original")
			}
		})
	}
}

func TestPrepareRequestCitationsAbsent(t *testing.T) {
	for _, raw := range []string{"", "null", " \n null ", "[]", "[ ]"} {
		for _, mediaType := range []string{"", "image/png"} {
			req := citationRequest(raw)
			req.Messages[0].Content[0].MediaType = mediaType
			got, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation)
			if err != nil {
				t.Fatal(err)
			}
			if got.Messages[0].Content[0].Citations != nil {
				t.Fatalf("%q not normalized to nil", raw)
			}
			if string(req.Messages[0].Content[0].Citations) != raw {
				t.Fatal("mutated absent metadata")
			}
		}
	}
	// Explicitly exercise the old persistence representation.
	var req Request
	if err := json.Unmarshal([]byte(`{"Messages":[{"Role":1,"Content":[{"Type":2,"Text":"old history","Citations":null}]}]}`), &req); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareRequestCitations(context.Background(), &req, "test-adapter", fakeAdaptCitation)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Content[0].Citations != nil || string(req.Messages[0].Content[0].Citations) != "null" {
		t.Fatal("old null history mishandled")
	}
}

func TestPrepareRequestCitationsErrors(t *testing.T) {
	for _, tt := range []struct{ name, raw, path, reason string }{
		{"unknown", `[{"type":"future_citation"}]`, "[0]", "unsupported citation type"},
		{"empty conversion", `[{"type":"empty"}]`, "[0]", "citation conversion returned no source reference"},
		{"malformed", `[{`, "", "expected citation array"},
		{"object", `{}`, "", "expected citation array"},
		{"whitespace", ` `, "", "expected citation array"},
		{"entry null", `[null]`, "[0]", "expected citation object"},
		{"entry array", `[[]]`, "[0]", "expected citation object"},
		{"entry number", `[1]`, "[0]", "expected citation object"},
		{"missing tag", `[{}]`, "[0]", "type must"},
		{"null tag", `[{"type":null}]`, "[0]", "type must"},
		{"wrong tag shape", `[{"type":{}}]`, "[0]", "type must"},
		{"mixed invalid", "[" + convertedCitation + `,{"type":"future"}]`, "[1]", "unsupported citation type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := citationRequest(tt.raw)
			got, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation)
			if got != nil || err == nil || !strings.Contains(err.Error(), "test-adapter") || !strings.Contains(err.Error(), "messages[0].content[0].citations"+tt.path) || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v, error %v", got, err)
			}
			if req.Messages[0].Content[0].Text != "Answer" || string(req.Messages[0].Content[0].Citations) != tt.raw {
				t.Fatal("error mutated history")
			}
		})
	}
	for _, content := range []Content{{Type: ContentTypeText, MediaType: "image/png"}, {Type: ContentTypeThinking}, {Type: ContentTypeToolResult}} {
		content.Citations = json.RawMessage("[" + convertedCitation + "]")
		req := &Request{Messages: []Message{{Content: []Content{content}}}}
		if _, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation); err == nil || !strings.Contains(err.Error(), "messages[0].content[0].citations[0]: citations require text") {
			t.Fatalf("non-text error = %v", err)
		}
	}
}

func TestPrepareRequestCitationsNestedAndLogs(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	req := citationRequest("[" + convertedCitation + "]")
	req.Messages = append(req.Messages, Message{Role: MessageRoleUser, Content: []Content{{Type: ContentTypeToolResult, ToolUseID: "call", ToolResult: []Content{{Type: ContentTypeText, Text: "Nested", Citations: json.RawMessage(`[{"type":"future"}]`)}}}}})
	if _, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation); err == nil || !strings.Contains(err.Error(), "messages[1].content[0].tool_result[0].citations[0]") {
		t.Fatalf("nested error = %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("logged success before complete validation: %s", logs.String())
	}
	req.Messages[1].Content[0].ToolResult[0].Citations = json.RawMessage("[" + convertedCitation + "]")
	got, err := PrepareRequestCitations(context.Background(), req, "test-adapter", fakeAdaptCitation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Messages[1].Content[0].ToolResult[0].Text, "Source title") {
		t.Fatal("nested citation not converted")
	}
	got.Messages[1].Content[0].ToolResult[0].Text = "changed"
	if req.Messages[1].Content[0].ToolResult[0].Text != "Nested" {
		t.Fatal("nested history aliases input")
	}
	for _, want := range []string{`"destination":"test-adapter"`, `"count":1`, `"types":["convert"]`, `messages[1].content[0].tool_result[0].citations`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log missing %s: %s", want, logs.String())
		}
	}
	for _, secret := range []string{"example.com", "Source title", "Nested"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaked %s", secret)
		}
	}
}

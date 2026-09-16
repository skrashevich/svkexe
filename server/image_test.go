package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestStripImageDataFromLLMData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		input        *llm.Message
		wantStripped bool
		wantHasData  bool // whether output should still have Data
	}{
		{
			name: "nil input",
		},
		{
			name: "text only message",
			input: &llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "hello"},
				},
			},
			wantStripped: false,
		},
		{
			name: "message with image data in content",
			input: &llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "description"},
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: strings.Repeat("x", 1000)},
				},
			},
			wantStripped: true,
			wantHasData:  false,
		},
		{
			name: "message with image data in tool result",
			input: &llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: "tool_1",
						ToolResult: []llm.Content{
							{Type: llm.ContentTypeText, Text: "Screenshot taken"},
							{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: strings.Repeat("x", 100000)},
						},
					},
				},
			},
			wantStripped: true,
			wantHasData:  false,
		},
		{
			name: "thinking data is not stripped",
			input: &llm.Message{
				Role: llm.MessageRoleAssistant,
				Content: []llm.Content{
					{Type: llm.ContentTypeThinking, Thinking: "thinking...", Data: "some-data", Signature: "sig"},
				},
			},
			wantStripped: false,
			wantHasData:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input *string
			if tt.input != nil {
				data, err := json.Marshal(tt.input)
				if err != nil {
					t.Fatal(err)
				}
				s := string(data)
				input = &s
			}

			result := stripImageDataFromLLMData(input, "msg-123")

			if tt.input == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", *result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if tt.wantStripped {
				if len(*result) >= len(*input) {
					t.Errorf("expected result to be smaller than input, got %d >= %d", len(*result), len(*input))
				}
			}

			var msg llm.Message
			if err := json.Unmarshal([]byte(*result), &msg); err != nil {
				t.Fatal(err)
			}

			hasData := false
			var checkData func([]llm.Content)
			checkData = func(contents []llm.Content) {
				for _, c := range contents {
					if c.Data != "" {
						hasData = true
					}
					checkData(c.ToolResult)
				}
			}
			checkData(msg.Content)

			if hasData != tt.wantHasData {
				t.Errorf("hasData = %v, want %v", hasData, tt.wantHasData)
			}
		})
	}
}

func TestStripImageDataInsertsURL(t *testing.T) {
	t.Parallel()
	// The stripped content should have a URL in the ImageURL field.
	msg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "description"},
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: "aGVsbG8="},
		},
	}
	data, _ := json.Marshal(msg)
	s := string(data)
	result := stripImageDataFromLLMData(&s, "msg-456")

	var parsed llm.Message
	if err := json.Unmarshal([]byte(*result), &parsed); err != nil {
		t.Fatal(err)
	}

	imgContent := parsed.Content[1]
	if imgContent.MediaType != "image/png" {
		t.Errorf("expected MediaType to be preserved, got %q", imgContent.MediaType)
	}
	if imgContent.Data != "" {
		t.Errorf("expected Data to be empty, got %q", imgContent.Data)
	}
	if imgContent.Text != "" {
		t.Errorf("expected Text to be empty, got %q", imgContent.Text)
	}
	wantURL := "/api/message/msg-456/image/1/-1"
	if imgContent.DisplayImageURL != wantURL {
		t.Errorf("expected ImageURL = %q, got %q", wantURL, imgContent.DisplayImageURL)
	}
}

func TestStripImageDataToolResult(t *testing.T) {
	t.Parallel()
	// Image inside a tool result should get the right URL.
	msg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool_1",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Screenshot taken"},
					{Type: llm.ContentTypeText, MediaType: "image/jpeg", Data: "base64data"},
				},
			},
		},
	}
	data, _ := json.Marshal(msg)
	s := string(data)
	result := stripImageDataFromLLMData(&s, "msg-789")

	var parsed llm.Message
	if err := json.Unmarshal([]byte(*result), &parsed); err != nil {
		t.Fatal(err)
	}

	imgContent := parsed.Content[0].ToolResult[1]
	wantURL := "/api/message/msg-789/image/0/1"
	if imgContent.DisplayImageURL != wantURL {
		t.Errorf("expected ImageURL = %q, got %q", wantURL, imgContent.DisplayImageURL)
	}
	if imgContent.Data != "" {
		t.Errorf("expected Data to be empty")
	}
	if imgContent.MediaType != "image/jpeg" {
		t.Errorf("expected MediaType preserved, got %q", imgContent.MediaType)
	}
}

func TestHandleMessageImage(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	// Create a conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Create a message with image data in a tool result
	imageData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	b64 := base64.StdEncoding.EncodeToString(imageData)

	msg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool_1",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Screenshot taken"},
					{Type: llm.ContentTypeText, MediaType: "image/png", Data: b64},
				},
			},
		},
	}

	createdMsg, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID,
		Type:           db.MessageTypeTool,
		LLMData:        msg,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Set up HTTP server
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Fetch the image via the endpoint
	resp, err := http.Get(httpServer.URL + "/api/message/" + createdMsg.MessageID + "/image/0/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("expected Content-Type image/png, got %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=1209600" {
		t.Errorf("expected 2-week Cache-Control, got %q", resp.Header.Get("Cache-Control"))
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, imageData) {
		t.Errorf("expected decoded image data, got %d bytes", len(body))
	}

	// Also verify that the conversation API strips the image data
	convResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	defer convResp.Body.Close()

	var streamResp StreamResponse
	if err := json.NewDecoder(convResp.Body).Decode(&streamResp); err != nil {
		t.Fatal(err)
	}

	for _, apiMsg := range streamResp.Messages {
		if apiMsg.LlmData == nil {
			continue
		}
		// The llm_data should not contain the raw base64 data
		if strings.Contains(*apiMsg.LlmData, b64) {
			t.Error("conversation API response still contains base64 image data")
		}
		// But should contain the image URL
		expectedURL := "/api/message/" + createdMsg.MessageID + "/image/0/1"
		if !strings.Contains(*apiMsg.LlmData, expectedURL) {
			t.Errorf("conversation API response should contain image URL %q", expectedURL)
		}
	}

	// Test invalid indices
	resp2, err := http.Get(httpServer.URL + "/api/message/" + createdMsg.MessageID + "/image/99/0")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for invalid index, got %d", resp2.StatusCode)
	}

	// Test nonexistent message
	resp3, err := http.Get(httpServer.URL + "/api/message/nonexistent/image/0/0")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent message, got %d", resp3.StatusCode)
	}
}

// TestLlmDataForAPISingleParse verifies that llmDataForAPI computes both the
// end-of-turn flag (for agent messages) and the image-stripped llm_data in a
// single JSON parse, matching the behavior of the previously-separate
// extractEndOfTurn and stripImageDataFromLLMData helpers.
func TestLlmDataForAPISingleParse(t *testing.T) {
	t.Parallel()

	msg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		EndOfTurn: true,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "here is a screenshot"},
			{Type: llm.ContentTypeText, MediaType: "image/png", Data: strings.Repeat("x", 1000)},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	// Agent message: end_of_turn pointer populated, image data stripped.
	gotData, gotEOT := llmDataForAPI(&s, string(db.MessageTypeAgent), "msg-eot")
	if gotEOT == nil || *gotEOT != true {
		t.Fatalf("expected end_of_turn=true, got %v", gotEOT)
	}
	if gotData == nil {
		t.Fatal("expected non-nil llm_data")
	}
	if len(*gotData) >= len(s) {
		t.Errorf("expected stripped data smaller than input, got %d >= %d", len(*gotData), len(s))
	}
	var parsed llm.Message
	if err := json.Unmarshal([]byte(*gotData), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Content[1].Data != "" {
		t.Errorf("expected image Data stripped, got %q", parsed.Content[1].Data)
	}
	if parsed.Content[1].DisplayImageURL != "/api/message/msg-eot/image/1/-1" {
		t.Errorf("unexpected image URL: %q", parsed.Content[1].DisplayImageURL)
	}

	// Non-agent message: no end_of_turn pointer, but still stripped.
	_, userEOT := llmDataForAPI(&s, string(db.MessageTypeUser), "msg-eot")
	if userEOT != nil {
		t.Errorf("expected nil end_of_turn for non-agent message, got %v", *userEOT)
	}

	// nil input returns (nil, nil).
	if d, e := llmDataForAPI(nil, string(db.MessageTypeAgent), "x"); d != nil || e != nil {
		t.Errorf("expected (nil, nil) for nil input, got (%v, %v)", d, e)
	}

	// No image: original pointer returned unchanged.
	plain := llm.Message{
		Role: llm.MessageRoleAssistant, EndOfTurn: false,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "no image"}},
	}
	pd, _ := json.Marshal(plain)
	ps := string(pd)
	gotPlain, gotPlainEOT := llmDataForAPI(&ps, string(db.MessageTypeAgent), "msg-plain")
	if gotPlain != &ps {
		t.Errorf("expected original pointer returned unchanged when nothing stripped")
	}
	if gotPlainEOT == nil || *gotPlainEOT != false {
		t.Errorf("expected end_of_turn=false, got %v", gotPlainEOT)
	}
}

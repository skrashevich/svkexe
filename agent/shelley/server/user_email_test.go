package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// findUserMessage returns the first user-type message in the conversation, or
// nil if none exists yet.
func findUserMessage(t *testing.T, database *db.DB, conversationID string) *generated.Message {
	t.Helper()
	var messages []generated.Message
	if err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	}); err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	for i := range messages {
		if messages[i].Type == string(db.MessageTypeUser) {
			return &messages[i]
		}
	}
	return nil
}

// TestChatStampsUserEmail verifies the X-ExeDev-Email header is persisted onto
// the user message row via the messages.user_email column on the immediate-send
// path.
func TestChatStampsUserEmail(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	body, _ := json.Marshal(ChatRequest{Message: "echo: hi", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ExeDev-Email", "alice@example.com")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	waitFor(t, 5*time.Second, func() bool {
		return findUserMessage(t, database, conversationID) != nil
	})
	msg := findUserMessage(t, database, conversationID)
	if msg.UserEmail == nil || *msg.UserEmail != "alice@example.com" {
		t.Fatalf("user message user_email = %v, want alice@example.com", msg.UserEmail)
	}
}

// TestChatWithoutEmailHeaderStoresNull verifies that a request lacking the
// X-ExeDev-Email header stores NULL (nil) in user_email rather than an empty
// string.
func TestChatWithoutEmailHeaderStoresNull(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	body, _ := json.Marshal(ChatRequest{Message: "echo: hi", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	waitFor(t, 5*time.Second, func() bool {
		return findUserMessage(t, database, conversationID) != nil
	})
	if msg := findUserMessage(t, database, conversationID); msg.UserEmail != nil {
		t.Fatalf("user message user_email = %q, want nil", *msg.UserEmail)
	}
}

// TestQueuedChatStampsUserEmail verifies the X-ExeDev-Email header captured at
// queue time survives onto the user row when the queued message drains. The
// drain runs on a background context (no request/header available), so the
// email must be carried in the persisted QueuedMessage entry.
func TestQueuedChatStampsUserEmail(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// First message from alice keeps the agent busy (delay).
	body1, _ := json.Marshal(ChatRequest{Message: "delay: 2", Model: "predictable"})
	req1 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body1)))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-ExeDev-Email", "alice@example.com")
	w1 := httptest.NewRecorder()
	server.handleChatConversation(w1, req1, conversationID)
	if w1.Code != http.StatusAccepted {
		t.Fatalf("first send: expected 202, got %d: %s", w1.Code, w1.Body.String())
	}

	// Wait for the agent to start working so the second message queues.
	waitFor(t, 5*time.Second, func() bool {
		return findUserMessage(t, database, conversationID) != nil
	})

	// Second message from bob is explicitly queued.
	body2, _ := json.Marshal(ChatRequest{Message: "echo: queued", Model: "predictable", Queue: true})
	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body2)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-ExeDev-Email", "bob@example.com")
	w2 := httptest.NewRecorder()
	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("queued send: expected 202, got %d: %s", w2.Code, w2.Body.String())
	}

	// The queued message drains into its own immutable user row carrying bob's
	// email. Wait until it exists and assert its email.
	var bobMsg *generated.Message
	waitFor(t, 10*time.Second, func() bool {
		var messages []generated.Message
		if err := database.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), conversationID)
			return qerr
		}); err != nil {
			return false
		}
		bobMsg = nil
		for i := range messages {
			if messages[i].Type != string(db.MessageTypeUser) || messages[i].LlmData == nil {
				continue
			}
			if strings.Contains(*messages[i].LlmData, "queued") {
				bobMsg = &messages[i]
			}
		}
		return bobMsg != nil
	})
	if bobMsg.UserEmail == nil || *bobMsg.UserEmail != "bob@example.com" {
		t.Fatalf("queued user message user_email = %v, want bob@example.com", bobMsg.UserEmail)
	}
}

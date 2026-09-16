package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// chatPost sends a /chat for conversationID and returns the recorder.
func chatPost(t *testing.T, server *Server, conversationID string, req ChatRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal chat request: %v", err)
	}
	httpReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, httpReq, conversationID)
	return w
}

// A push "Reply" (and any client that omits the model, e.g. the iOS
// inline-reply handler) used to silently 400 on conversations whose
// model differs from the host's effective default: the empty model was
// resolved to effectiveDefaultModel, then ensureLoop rejected the send
// with errConversationModelMismatch. The fix: when the client omits a
// model on a conversation with a persisted model, defer to that model
// instead of the host default.
func TestChatEmptyModelUsesConversationModel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// First message loads the loop on a model that is NOT the host's
	// effective default ("predictable"). This mirrors a real conversation
	// the user explicitly ran on a non-default model.
	if w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: hi", Model: "some-other-model"}); w.Code != http.StatusAccepted {
		t.Fatalf("first send: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// ensureLoop persists the model synchronously before the handler
	// returns 202 (it writes the model, then spawns the loop goroutine),
	// so the second send below can resolve it without any wait. Assert
	// that invariant explicitly rather than relying on a sleep.
	got, err := database.GetConversationByID(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Model == nil || *got.Model != "some-other-model" {
		t.Fatalf("model not persisted after first send: got %v", got.Model)
	}

	// Now a reply that omits the model — exactly what the push reply
	// handler sends. This must NOT 400 with a model mismatch.
	if w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: reply from watch"}); w.Code != http.StatusAccepted {
		t.Fatalf("reply with empty model: expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

// A draft created with a non-default model is the same hazard: drafts
// persist a model at creation, so an omitted-model send must resolve to
// that model rather than the host default. Before the fix, the draft
// branch was excluded from persisted-model resolution; the first send
// promoted the draft and silently pinned the loop to the default, and a
// follow-up omitted-model send then 400'd with a model mismatch.
func TestChatEmptyModelUsesDraftModel(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	model := "some-other-model"
	draft, err := database.CreateDraftConversation(context.Background(), nil, &model, db.ConversationOptions{}, "echo: draft body")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}
	conversationID := draft.ConversationID

	// First omitted-model send promotes the draft. It must pin the loop to
	// the draft's persisted model, not the host default.
	if w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: hi"}); w.Code != http.StatusAccepted {
		t.Fatalf("promote send: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Second omitted-model send (the push reply) must match the pinned
	// model and not 400.
	if w := chatPost(t, server, conversationID, ChatRequest{Message: "echo: reply from watch"}); w.Code != http.StatusAccepted {
		t.Fatalf("reply with empty model: expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

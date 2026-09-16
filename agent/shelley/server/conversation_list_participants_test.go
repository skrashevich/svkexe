package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

// TestConversationListParticipants verifies that the conversation list carries
// the exe.dev accounts that authored messages, both in the /api/stream2 patch
// stream (so clients can filter to "my" conversations without refetching) and
// in the snapshot the stream is seeded from.
func TestConversationListParticipants(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), strPtr("participants"), true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conversation.ConversationID

	ctx, cancel := context.WithCancel(context.Background())
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { server.handleStream(rec, req); close(done) }()
	defer func() { cancel(); <-done }()

	initial := waitForPatchEventAfter(t, rec, "")
	state := mustApplyPatch(t, []ConversationWithState{}, initial.Patch)
	if len(state) != 1 || state[0].Participants != nil {
		t.Fatalf("expected one conversation with no participants, got %+v", state)
	}

	// Two different accounts send a message each; the list should end up
	// listing both, sorted, with authored-message counts.
	for _, email := range []string{"bob@example.com", "alice@example.com"} {
		body, _ := json.Marshal(ChatRequest{Message: "echo: hi", Model: "predictable"})
		w := httptest.NewRecorder()
		chatReq := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
		chatReq.Header.Set("Content-Type", "application/json")
		chatReq.Header.Set("X-ExeDev-Email", email)
		server.handleChatConversation(w, chatReq, conversationID)
		if w.Code != http.StatusAccepted {
			t.Fatalf("chat as %s: expected 202, got %d: %s", email, w.Code, w.Body.String())
		}
	}

	// Walk the patch stream forward until both participants have landed,
	// applying every op so the frames are proven self-consistent.
	want := []db.ConversationParticipant{
		{Email: "alice@example.com", MessageCount: 1},
		{Email: "bob@example.com", MessageCount: 1},
	}
	prev := initial.NewHash
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Equal(state[0].Participants, want) {
		if time.Now().After(deadline) {
			t.Fatalf("participants = %v, want %v", state[0].Participants, want)
		}
		ev := waitForPatchEventAfter(t, rec, prev)
		state = mustApplyPatch(t, state, ev.Patch)
		verifyHash(t, state, ev.NewHash)
		prev = ev.NewHash
	}

	// The snapshot the stream is seeded from carries the same participants.
	w := httptest.NewRecorder()
	server.handleConversationsSnapshot(w, httptest.NewRequest(http.MethodGet, "/api/conversations/snapshot", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var snapshot ConversationListSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conversations) != 1 {
		t.Fatalf("snapshot: expected 1 conversation, got %d", len(snapshot.Conversations))
	}
	if !slices.Equal(snapshot.Conversations[0].Participants, want) {
		t.Fatalf("snapshot participants = %v, want %v", snapshot.Conversations[0].Participants, want)
	}
}

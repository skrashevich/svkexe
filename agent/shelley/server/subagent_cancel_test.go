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
	"shelley.exe.dev/llm"
)

// startSlowTurn starts a long-running turn (predictable "bash: sleep") on the
// given conversation and waits until the agent is working on it.
func startSlowTurn(t *testing.T, server *Server, conversationID string) {
	t.Helper()
	chatBody, _ := json.Marshal(ChatRequest{Message: "bash: sleep 30", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		return server.IsAgentWorking(conversationID)
	})
}

// cancelConversation POSTs to the cancel endpoint for the given conversation.
func cancelConversation(t *testing.T, server *Server, conversationID string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	w := httptest.NewRecorder()
	server.handleCancelConversation(w, req, conversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
}

// hasCancelledEndOfTurn reports whether the conversation contains the
// synthetic "[Operation cancelled]" end-of-turn message.
func hasCancelledEndOfTurn(t *testing.T, database *db.DB, conversationID string) bool {
	t.Helper()
	var messages []generated.Message
	err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, msg := range messages {
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		if !llmMsg.EndOfTurn {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "Operation cancelled") {
				return true
			}
		}
	}
	return false
}

// TestCancelParentCancelsRunningSubagent verifies that cancelling a parent
// conversation also cancels its actively-working subagent conversations, even
// when the parent itself has no active loop (e.g. it was evicted while the
// subagent kept working).
func TestCancelParentCancelsRunningSubagent(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	parentConv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	subConv, err := database.CreateSubagentConversation(ctx, "sub-cancel", parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create subagent: %v", err)
	}

	startSlowTurn(t, server, subConv.ConversationID)

	cancelConversation(t, server, parentConv.ConversationID)

	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(subConv.ConversationID)
	})
	if !hasCancelledEndOfTurn(t, database, subConv.ConversationID) {
		t.Error("expected subagent to have an '[Operation cancelled]' end-of-turn message")
	}
}

// TestCancelParentCancelsSubagentTree verifies that cancellation propagates
// recursively: cancelling the root also cancels a working grandchild.
func TestCancelParentCancelsSubagentTree(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	rootConv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	childConv, err := database.CreateSubagentConversation(ctx, "child", rootConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchildConv, err := database.CreateSubagentConversation(ctx, "grandchild", childConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}

	startSlowTurn(t, server, rootConv.ConversationID)
	startSlowTurn(t, server, grandchildConv.ConversationID)

	cancelConversation(t, server, rootConv.ConversationID)

	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(rootConv.ConversationID) && !server.IsAgentWorking(grandchildConv.ConversationID)
	})
	if !hasCancelledEndOfTurn(t, database, grandchildConv.ConversationID) {
		t.Error("expected grandchild to have an '[Operation cancelled]' end-of-turn message")
	}
}

// TestCancelledSubagentOwesNoNotification verifies that a subagent turn torn
// down by CancelConversation while a synchronous (wait=true) waiter holds a
// slot is NOT recorded as a suppressed finish: the waiter that later gives up
// without delivering must not owe the parent an async completion notification
// for a turn the user cut short.
func TestCancelledSubagentOwesNoNotification(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	parentConv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	subConv, err := database.CreateSubagentConversation(ctx, "sub-waiter", parentConv.ConversationID, nil)
	if err != nil {
		t.Fatalf("create subagent: %v", err)
	}

	startSlowTurn(t, server, subConv.ConversationID)

	server.mu.Lock()
	mgr := server.activeConversations[subConv.ConversationID]
	server.mu.Unlock()
	if mgr == nil {
		t.Fatal("expected active subagent manager")
	}

	// Simulate a wait=true subagent tool call in flight against this subagent.
	mgr.registerSubagentWaiter()

	if err := mgr.CancelConversation(ctx); err != nil {
		t.Fatalf("cancel subagent: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(subConv.ConversationID)
	})

	// The waiter gives up without delivering (e.g. its context was cancelled
	// along with the parent). A cancellation is not a completion, so no async
	// notification is owed.
	if notifyOwed := mgr.finishSubagentWait(false); notifyOwed {
		t.Error("expected no notification owed after cancellation, got notifyOwed=true")
	}
}

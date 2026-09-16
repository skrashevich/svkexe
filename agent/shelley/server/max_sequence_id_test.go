package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestMaxSequenceIDInConversationsList: /api/conversations rows expose
// max_sequence_id matching the latest message sequence_id.
func TestMaxSequenceIDInConversationsList(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	convID := seedConversation(t, database, 3)

	_, srv := newTestStreamServer(t, database)

	req := httptest.NewRequest("GET", "/api/conversations", nil)
	w := httptest.NewRecorder()
	srv.handleConversations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var convs []ConversationWithState
	if err := json.Unmarshal(w.Body.Bytes(), &convs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found *ConversationWithState
	for i := range convs {
		if convs[i].ConversationID == convID {
			found = &convs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("conversation %s not in list", convID)
	}

	// Cross-check via the bulk per-conv query.
	maxSeqs, err := database.GetMaxSequenceIDsForAllConversations(context.Background())
	if err != nil {
		t.Fatalf("GetMaxSequenceIDsForAllConversations: %v", err)
	}
	maxSeq := maxSeqs[convID]
	if maxSeq <= 0 {
		t.Fatalf("expected positive maxSeq, got %d", maxSeq)
	}
	if found.MaxSequenceID != maxSeq {
		t.Fatalf("max_sequence_id mismatch: got %d want %d", found.MaxSequenceID, maxSeq)
	}
}

// TestMaxSequenceIDOnGetConversation: GET /api/conversation/<id> returns a
// max_sequence_id matching the highest sequence_id across the returned
// messages.
func TestMaxSequenceIDOnGetConversation(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	convID := seedConversation(t, database, 4)

	_, srv := newTestStreamServer(t, database)

	req := httptest.NewRequest("GET", "/api/conversation/"+convID, nil)
	w := httptest.NewRecorder()
	srv.handleGetConversation(w, req, convID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StreamResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Messages) == 0 {
		t.Fatalf("expected messages, got none")
	}
	var want int64
	for _, m := range resp.Messages {
		if m.SequenceID > want {
			want = m.SequenceID
		}
	}
	if resp.MaxSequenceID != want {
		t.Fatalf("max_sequence_id mismatch: got %d want %d", resp.MaxSequenceID, want)
	}
}

// TestGetConversationLastSequenceID: GET /api/conversation/<id> honors
// the `last_sequence_id` query param and returns only messages strictly
// newer than the cursor. Mirrors the SSE stream's resume semantics so
// iOS can reuse the same cursor when REST-hydrating a partially cached
// conversation under the stream2 architecture.
func TestGetConversationLastSequenceID(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	convID := seedConversation(t, database, 5)

	_, srv := newTestStreamServer(t, database)

	// Establish the seq baseline from a no-cursor fetch.
	req := httptest.NewRequest("GET", "/api/conversation/"+convID, nil)
	w := httptest.NewRecorder()
	srv.handleGetConversation(w, req, convID)
	var full StreamResponse
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(full.Messages) < 3 {
		t.Fatalf("expected >=3 seeded messages, got %d", len(full.Messages))
	}
	cursor := full.Messages[len(full.Messages)-3].SequenceID

	// Refetch with cursor; expect only messages strictly newer.
	req2 := httptest.NewRequest("GET", "/api/conversation/"+convID+"?last_sequence_id="+strconv.FormatInt(cursor, 10), nil)
	w2 := httptest.NewRecorder()
	srv.handleGetConversation(w2, req2, convID)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var tail StreamResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &tail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tail.Messages) >= len(full.Messages) {
		t.Fatalf("cursor=%d did not trim: got %d msgs (full=%d)", cursor, len(tail.Messages), len(full.Messages))
	}
	for _, m := range tail.Messages {
		if m.SequenceID <= cursor {
			t.Fatalf("got message seq=%d <= cursor=%d", m.SequenceID, cursor)
		}
	}

	// Cursor at or above the latest -> empty tail (no error).
	req3 := httptest.NewRequest("GET", "/api/conversation/"+convID+"?last_sequence_id="+strconv.FormatInt(full.MaxSequenceID, 10), nil)
	w3 := httptest.NewRecorder()
	srv.handleGetConversation(w3, req3, convID)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w3.Code, w3.Body.String())
	}
	var empty StreamResponse
	if err := json.Unmarshal(w3.Body.Bytes(), &empty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(empty.Messages) != 0 {
		t.Fatalf("expected empty tail, got %d msgs", len(empty.Messages))
	}
}

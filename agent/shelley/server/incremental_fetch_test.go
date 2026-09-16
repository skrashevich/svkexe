package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// seedConversation creates a conversation with N alternating user/agent
// messages and returns the conversation id.
func seedConversation(t *testing.T, database *db.DB, n int) string {
	t.Helper()
	ctx := context.Background()
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	for i := 0; i < n; i++ {
		role := llm.MessageRoleUser
		msgType := db.MessageTypeUser
		if i%2 == 1 {
			role = llm.MessageRoleAssistant
			msgType = db.MessageTypeAgent
		}
		_, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           msgType,
			LLMData: llm.Message{
				Role:    role,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: "m" + string(rune('0'+i))}},
			},
		})
		if err != nil {
			t.Fatalf("CreateMessage[%d]: %v", i, err)
		}
	}
	return conv.ConversationID
}

func newTestStreamServer(t *testing.T, database *db.DB) (*http.ServeMux, *Server) {
	t.Helper()
	ps := predictable.NewService()
	srv := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", "")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return mux, srv
}

// runStreamWithQuery opens the per-conversation SSE stream with the
// given query string and returns all `data:` frames decoded as
// StreamResponse in order.
func runStreamWithQuery(t *testing.T, srv *Server, convID, query string, maxFrames int) []StreamResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "/api/conversation/" + convID + "/stream"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil).WithContext(ctx)
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleStreamConversation(w, req, convID)
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body := w.Snapshot()
		frames := decodeSSE(body)
		if len(frames) >= maxFrames {
			return frames[:maxFrames]
		}
		// snapshot_complete (initial-batch boundary) is the natural
		// stopping point for read-only queries; return whatever we've
		// collected so far.
		for _, f := range frames {
			if f.SnapshotComplete {
				return frames
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runStreamWithQuery: did not see snapshot_complete or %d frames within timeout; body: %s", maxFrames, w.Snapshot())
	return nil
}

func decodeSSE(body string) []StreamResponse {
	var out []StreamResponse
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		frame = strings.TrimPrefix(frame, "data: ")
		if frame == "" {
			continue
		}
		var sr StreamResponse
		if err := json.Unmarshal([]byte(frame), &sr); err != nil {
			continue
		}
		out = append(out, sr)
	}
	return out
}

// TestStreamSnapshotComplete: every per-conversation stream should emit a
// snapshot_complete frame after the initial batch.
func TestStreamSnapshotComplete(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	id := seedConversation(t, database, 3)
	_, srv := newTestStreamServer(t, database)

	frames := runStreamWithQuery(t, srv, id, "", 10)
	var gotInitial, gotComplete bool
	for _, f := range frames {
		if len(f.Messages) == 3 {
			gotInitial = true
		}
		if f.SnapshotComplete {
			gotComplete = true
		}
	}
	if !gotInitial {
		t.Errorf("expected an initial frame with 3 messages; frames: %+v", frames)
	}
	if !gotComplete {
		t.Errorf("expected snapshot_complete frame; frames: %+v", frames)
	}
}

// TestStreamTailParam: tail=N delivers only the last N messages, ascending,
// followed by snapshot_complete.
func TestStreamTailParam(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	id := seedConversation(t, database, 5)
	_, srv := newTestStreamServer(t, database)

	frames := runStreamWithQuery(t, srv, id, "tail=2", 10)
	var initial *StreamResponse
	for i := range frames {
		if len(frames[i].Messages) > 0 {
			initial = &frames[i]
			break
		}
	}
	if initial == nil {
		t.Fatalf("no initial messages frame; frames: %+v", frames)
	}
	if len(initial.Messages) != 2 {
		t.Fatalf("want 2 messages on tail=2, got %d", len(initial.Messages))
	}
	if initial.Messages[1].SequenceID != 5 {
		t.Errorf("want last seq=5, got %d", initial.Messages[1].SequenceID)
	}
	if initial.ContextWindowSize != 0 {
		t.Errorf("want context_window_size omitted on tail stream, got %d", initial.ContextWindowSize)
	}
}

// TestStreamLastSeqBeyondMax: requesting last_sequence_id beyond MAX(seq)
// is a legal no-op — sequence ids never decrease, generations don't
// truncate history, so the cursor is just "caught up". We expect no
// messages frame and a snapshot_complete to mark we're idle.
func TestStreamLastSeqBeyondMax(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	id := seedConversation(t, database, 3)
	_, srv := newTestStreamServer(t, database)

	frames := runStreamWithQuery(t, srv, id, "last_sequence_id=99", 10)
	var gotComplete bool
	for _, f := range frames {
		if len(f.Messages) > 0 {
			t.Errorf("unexpected messages with last_sequence_id=99: %+v", f.Messages)
		}
		if f.SnapshotComplete {
			gotComplete = true
		}
	}
	if !gotComplete {
		t.Errorf("expected snapshot_complete frame; frames: %+v", frames)
	}
}

// TestStreamTailAndLastSeqIsBadRequest: combining the two cursors
// is a client bug, not silently massaged into one or the other.
func TestStreamTailAndLastSeqIsBadRequest(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	id := seedConversation(t, database, 3)
	_, srv := newTestStreamServer(t, database)

	req := httptest.NewRequest("GET", "/api/conversation/"+id+"/stream?tail=1&last_sequence_id=0", nil)
	w := httptest.NewRecorder()
	srv.handleStreamConversation(w, req, id)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d; body=%q", w.Code, w.Body.String())
	}
}

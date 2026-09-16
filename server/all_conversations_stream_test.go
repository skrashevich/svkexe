package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// hasMessageText reports whether any message in the frame's LlmData JSON
// blob mentions the given substring. APIMessage carries content as a
// serialized llm.Message in LlmData; that's enough to fingerprint a
// message body we just wrote.
func hasMessageText(f StreamResponse, text string) bool {
	for _, m := range f.Messages {
		if m.LlmData != nil && strings.Contains(*m.LlmData, text) {
			return true
		}
	}
	return false
}

// runUnifiedStream opens /api/stream2 with the given query string and polls
// the response body until `until(frames)` returns true or the deadline
// fires. Returns the decoded frames collected so far. The handler is
// cancelled and drained before returning.
func runUnifiedStream(t *testing.T, srv *Server, query string, until func([]StreamResponse) bool, timeout time.Duration) []StreamResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := "/api/stream2"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx)
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleStream(w, req)
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()

	deadline := time.Now().Add(timeout)
	var frames []StreamResponse
	for time.Now().Before(deadline) {
		frames = decodeSSE(w.Snapshot())
		if until(frames) {
			return frames
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runUnifiedStream: timed out waiting for condition; got %d frames; body=%s", len(frames), w.Snapshot())
	return frames
}

// createLiveMessage adds a message to an *already active* conversation and
// fires the same notify path the live server would: the message arrives on
// streamPub tagged with conversation_id.
func createLiveMessage(t *testing.T, srv *Server, database *db.DB, convID, text string) {
	t.Helper()
	ctx := context.Background()
	msg, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: convID,
		Type:           db.MessageTypeUser,
		LLMData: llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: text}},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessage(%s): %v", convID, err)
	}
	srv.notifySubscribersNewMessage(ctx, convID, msg)
}

// TestUnifiedStreamMultiplexesTwoConversations: a single /api/stream2
// connection (no ?conversation= backfill) receives live message events for
// every active conversation, each tagged with the correct conversation_id.
// This is the headline guarantee of the refactor.
func TestUnifiedStreamMultiplexesTwoConversations(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)

	convA := seedConversation(t, database, 0)
	convB := seedConversation(t, database, 0)
	// Activate managers so notifySubscribersNewMessage will publish.
	if _, err := srv.getOrCreateConversationManager(context.Background(), convA, ""); err != nil {
		t.Fatalf("activate A: %v", err)
	}
	if _, err := srv.getOrCreateConversationManager(context.Background(), convB, ""); err != nil {
		t.Fatalf("activate B: %v", err)
	}

	// Open stream, then produce a message on A and on B.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx)
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleStream(w, req)
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()

	// Wait until at least one frame has been flushed so the subscriber is
	// registered (the initial list-patch event always fires).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(decodeSSE(w.Snapshot())) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	createLiveMessage(t, srv, database, convA, "hello-A")
	createLiveMessage(t, srv, database, convB, "hello-B")

	// Poll until we see at least one message frame for A and one for B.
	var sawA, sawB bool
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(sawA && sawB) {
		for _, f := range decodeSSE(w.Snapshot()) {
			if len(f.Messages) == 0 {
				continue
			}
			switch f.ConversationID {
			case convA:
				if hasMessageText(f, "hello-A") {
					sawA = true
				}
			case convB:
				if hasMessageText(f, "hello-B") {
					sawB = true
				}
			default:
				t.Fatalf("message frame missing conversation_id: %+v", f)
			}
		}
		if sawA && sawB {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawA {
		t.Errorf("did not see live message tagged with conv A=%s; body=%s", convA, w.Snapshot())
	}
	if !sawB {
		t.Errorf("did not see live message tagged with conv B=%s; body=%s", convB, w.Snapshot())
	}
}

// TestUnifiedStreamInitialBackfillScopedToOneConversation:
// /api/stream2?conversation=A backfills A's history only — B's messages do
// not appear in the initial replay, even though both conversations have
// messages on disk.
func TestUnifiedStreamInitialBackfillScopedToOneConversation(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)

	convA := seedConversation(t, database, 3)
	convB := seedConversation(t, database, 4)

	frames := runUnifiedStream(t, srv, "conversation="+convA, func(fs []StreamResponse) bool {
		// Stop once we've seen the snapshot_complete marker; the backfill
		// frame must precede it.
		for _, f := range fs {
			if f.SnapshotComplete {
				return true
			}
		}
		return false
	}, 5*time.Second)

	var backfill *StreamResponse
	for i := range frames {
		if len(frames[i].Messages) > 0 {
			backfill = &frames[i]
			break
		}
	}
	if backfill == nil {
		t.Fatalf("no backfill frame with messages; frames=%+v", frames)
	}
	if backfill.ConversationID != convA {
		t.Errorf("backfill conversation_id: got %q want %q", backfill.ConversationID, convA)
	}
	if len(backfill.Messages) != 3 {
		t.Errorf("backfill should carry A's 3 messages, got %d", len(backfill.Messages))
	}
	// And no frame before snapshot_complete may reference convB messages.
	for _, f := range frames {
		if f.SnapshotComplete {
			break
		}
		if f.ConversationID == convB && len(f.Messages) > 0 {
			t.Errorf("backfill leaked conversation B messages: %+v", f)
		}
	}
}

// TestUnifiedStreamReceivesCrossConversationLiveEvents:
// /api/stream2?conversation=A backfills A but the SAME connection still
// receives live events for *other* conversations, tagged with their
// conversation_id. This is the multiplexing guarantee for the ?conversation
// flavor of the endpoint.
func TestUnifiedStreamReceivesCrossConversationLiveEvents(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)

	convA := seedConversation(t, database, 1)
	convB := seedConversation(t, database, 0)
	if _, err := srv.getOrCreateConversationManager(context.Background(), convB, ""); err != nil {
		t.Fatalf("activate B: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2?conversation="+convA, nil).WithContext(ctx)
	w := newResponseRecorderWithClose()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleStream(w, req)
	}()
	defer func() {
		w.Close()
		cancel()
		<-done
	}()

	// Wait for snapshot_complete so we know the live-only phase has begun.
	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		for _, f := range decodeSSE(w.Snapshot()) {
			if f.SnapshotComplete {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("never saw snapshot_complete; body=%s", w.Snapshot())
	}

	createLiveMessage(t, srv, database, convB, "crossover-B")

	deadline = time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) && !found {
		for _, f := range decodeSSE(w.Snapshot()) {
			if f.ConversationID == convB && hasMessageText(f, "crossover-B") {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !found {
		t.Errorf("did not see cross-conversation B event on /api/stream2?conversation=A; body=%s", w.Snapshot())
	}
}

// TestLegacyConversationStreamIsolatedFromOtherConversations:
// /api/conversation/A/stream is the legacy iOS/CLI endpoint. It must keep
// receiving messages only for A — live events on B must NOT leak into this
// per-conversation subpub. Regression guard for the refactor.
func TestLegacyConversationStreamIsolatedFromOtherConversations(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		srv, database, _ := newTestServer(t)

		convA := seedConversation(t, database, 1)
		convB := seedConversation(t, database, 0)
		if _, err := srv.getOrCreateConversationManager(context.Background(), convB, ""); err != nil {
			t.Fatalf("activate B: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/api/conversation/"+convA+"/stream", nil).WithContext(ctx)
		w := newResponseRecorderWithClose()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.handleStreamConversation(w, req, convA)
		}()
		defer func() {
			w.Close()
			cancel()
			<-done
		}()

		// Wait for snapshot_complete before producing the cross-conversation event.
		deadline := time.Now().Add(5 * time.Second)
		var ready bool
		for time.Now().Before(deadline) {
			for _, f := range decodeSSE(w.Snapshot()) {
				if f.SnapshotComplete {
					ready = true
					break
				}
			}
			if ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !ready {
			t.Fatalf("never saw snapshot_complete on legacy endpoint; body=%s", w.Snapshot())
		}

		createLiveMessage(t, srv, database, convB, "should-not-leak")

		// Let any (incorrect) leak materialize.
		synctest.Wait()

		for _, f := range decodeSSE(w.Snapshot()) {
			if len(f.Messages) == 0 {
				continue
			}
			if f.ConversationID == convB {
				t.Errorf("legacy /api/conversation/%s/stream leaked a message tagged with B=%s: %+v", convA, convB, f)
			}
			if hasMessageText(f, "should-not-leak") {
				t.Errorf("legacy endpoint received conversation B message body: %+v", f)
			}
		}
	})
}

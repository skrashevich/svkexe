package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestPredictableFailRecordsWarningMessage(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	h.NewConversation("fail nope", "")

	var messages []string
	waitFor(t, 5*time.Second, func() bool {
		msgs, err := h.db.ListMessages(context.Background(), h.ConversationID())
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		messages = messages[:0]
		for _, msg := range msgs {
			if msg.Type != string(db.MessageTypeWarning) || msg.UserData == nil {
				continue
			}
			var userData struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
				t.Fatalf("warning user_data: %v", err)
			}
			messages = append(messages, userData.Text)
		}
		return len(messages) == 1 && strings.Contains(messages[0], "nope")
	})
}

// TestWarningMessageReachesUnifiedStream asserts that a warning message is
// delivered on /api/stream2, the stream the web UI actually listens to.
//
// Warnings are recorded by loop.recordRetryWarning on LLM retry / overload /
// rate-limit events — ordinary operation, not damage. They get a real
// sequence_id from GetNextSequenceID, so if they are published only to the
// per-conversation subpub (the legacy /api/conversation/<id>/stream endpoint)
// and not to the server-wide streamPub, the web client never sees them AND its
// view of the sequence space acquires a hole: seq N (warning) is missing while
// seq N+1 arrives. The client's message cache detects that skip and correctly
// forfeits its cached history, so on a flaky-LLM day this would systematically
// force full conversation re-downloads (see ui/src/services/messageStore.ts).
func TestWarningMessageReachesUnifiedStream(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	h.NewConversation("hello", "")

	// Subscribe to the server-wide stream the way handleStream does, before
	// triggering the warning.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next := h.server.streamPub.Subscribe(ctx, -1)

	// "fail <text>" makes the predictable service fail and retry, which is
	// what produces a retry warning.
	h.Chat("fail nope")

	deadline := time.Now().Add(10 * time.Second)
	type frame struct {
		data StreamResponse
		ok   bool
	}
	frames := make(chan frame)
	go func() {
		for {
			d, ok := next()
			frames <- frame{d, ok}
			if !ok {
				return
			}
		}
	}()
	for {
		select {
		case f := <-frames:
			if !f.ok {
				t.Fatal("stream closed before the warning arrived")
			}
			for _, m := range f.data.Messages {
				if m.Type != string(db.MessageTypeWarning) {
					continue
				}
				if m.SequenceID <= 0 {
					t.Errorf("warning has sequence_id %d", m.SequenceID)
				}
				return // delivered
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("warning message never arrived on the unified stream; recordWarning must use publishStream so it reaches streamPub, not just the per-conversation subpub")
		}
	}
}

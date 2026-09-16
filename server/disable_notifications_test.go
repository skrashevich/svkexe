package server

import (
	"context"
	"sync"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/notifications"
)

// recordingChannel is a notifications.Channel that records every event it
// receives, so tests can assert whether an end-of-turn notification fired.
type recordingChannel struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (c *recordingChannel) Name() string { return "recording" }

func (c *recordingChannel) Send(ctx context.Context, event notifications.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *recordingChannel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func TestPublishConversationStateHonorsDisableNotifications(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		opts    db.ConversationOptions
		wantHit bool
	}{
		{"default notifies", db.ConversationOptions{}, true},
		{"disabled suppresses", db.ConversationOptions{DisableNotifications: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, database, _ := newTestServer(t)
			ch := &recordingChannel{}
			server.RegisterNotificationChannel(ch)

			conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, tc.opts)
			if err != nil {
				t.Fatalf("failed to create conversation: %v", err)
			}

			// Simulate the agent finishing a turn.
			server.publishConversationState(ConversationState{
				ConversationID: conversation.ConversationID,
				Working:        false,
				Model:          "predictable",
			})

			got := ch.count() > 0
			if got != tc.wantHit {
				t.Fatalf("notification fired = %v, want %v (events=%d)", got, tc.wantHit, ch.count())
			}
		})
	}
}

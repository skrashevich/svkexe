package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

func writeSlashHook(t *testing.T, name, script string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "shelley", "hooks", "slash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestChatSlashCommandHookReplacesMessage verifies that posting a message of
// the form "/<cmd> args..." invokes ~/.config/shelley/hooks/slash/<cmd>, and
// the hook's stdout becomes the recorded user-message text.
func TestChatSlashCommandHookReplacesMessage(t *testing.T) {
	writeSlashHook(t, "shout", `#!/bin/sh
read line
# Uppercase the args via tr.
printf 'SHOUT: %s' "$SHELLEY_SLASH_ARGS" | tr a-z A-Z
`)

	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conv.ConversationID

	body, _ := json.Marshal(ChatRequest{Message: "/shout hello world", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// Wait briefly for the user message to be recorded.
	var userMsg generated.Message
	for i := 0; i < 50; i++ {
		var msgs []generated.Message
		err = database.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			msgs, qerr = q.ListMessages(context.Background(), conversationID)
			return qerr
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Type == "user" {
				userMsg = m
				break
			}
		}
		if userMsg.ConversationID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if userMsg.ConversationID == "" {
		t.Fatal("user message was never recorded")
	}
	var payload string
	if userMsg.LlmData != nil {
		payload = *userMsg.LlmData
	}
	if !strings.Contains(payload, "SHOUT: HELLO WORLD") {
		t.Errorf("expected hook output in recorded message, got: %s", payload)
	}
}

// TestChatSlashCommandHookErrorReturns400 verifies that a failing slash hook
// surfaces a client error and does not record any message.
func TestChatSlashCommandHookErrorReturns400(t *testing.T) {
	writeSlashHook(t, "boom", "#!/bin/sh\necho boom 1>&2\nexit 2\n")

	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ChatRequest{Message: "/boom arg", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conv.ConversationID+"/chat", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conv.ConversationID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 from failing slash hook, got %d: %s", w.Code, w.Body.String())
	}
}

// TestChatSlashCommandNoHookPassthrough verifies that messages with slashes
// but no matching hook are treated as normal user messages.
func TestChatSlashCommandNoHookPassthrough(t *testing.T) {
	// Point HOME at an empty temp dir so no hooks exist.
	t.Setenv("HOME", t.TempDir())

	server, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ChatRequest{Message: "/unknown please", Model: "predictable"})
	req := httptest.NewRequest("POST", "/api/conversation/"+conv.ConversationID+"/chat", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conv.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 passthrough, got %d: %s", w.Code, w.Body.String())
	}
}

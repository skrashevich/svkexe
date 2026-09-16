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
)

// TestGenerateLoremConversation verifies the synthetic conversation
// generator produces a loadable conversation containing every message type
// the UI renders, and that the /debug/loremipsum handler persists and
// redirects to it.
func TestGenerateLoremConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	// Use enough turns to trigger at least two compactions (compactEvery=40).
	convID, err := server.generateLoremConversation(ctx, 100, "claude-opus-4-5")
	if err != nil {
		t.Fatalf("generateLoremConversation: %v", err)
	}

	// Every message type should be represented. Tool calls and results are
	// embedded in agent (tool_use) and user (tool_result) messages, so the
	// standalone "tool" message type is intentionally not produced — this
	// matches how the real loop records messages. System messages appear
	// once per generation.
	for _, mt := range []db.MessageType{
		db.MessageTypeUser, db.MessageTypeAgent, db.MessageTypeSystem,
		db.MessageTypeGitInfo, db.MessageTypeWarning, db.MessageTypeError,
		db.MessageTypeModelChange,
	} {
		msgs, err := database.ListMessagesByType(ctx, convID, mt)
		if err != nil {
			t.Fatalf("ListMessagesByType(%s): %v", mt, err)
		}
		if len(msgs) == 0 {
			t.Errorf("no messages of type %s were generated", mt)
		}
	}

	// The conversation must load through the real handler and its messages
	// must unmarshal (this is what the UI does).
	req := httptest.NewRequest("GET", "/api/conversation/"+convID, nil)
	w := httptest.NewRecorder()
	server.handleGetConversation(w, req, convID)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGetConversation status = %d, body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Messages []struct {
			MessageID string  `json:"message_id"`
			LLMData   *string `json:"llm_data"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal conversation response: %v", err)
	}
	if len(resp.Messages) < 40 {
		t.Fatalf("expected many messages, got %d", len(resp.Messages))
	}

	// Every message's llm_data must be valid JSON so the client can parse it.
	toolUses := 0
	for _, m := range resp.Messages {
		if m.LLMData == nil {
			continue
		}
		var v struct {
			Content []struct {
				Type     int    `json:"Type"`
				ToolName string `json:"ToolName"`
			} `json:"Content"`
		}
		if err := json.Unmarshal([]byte(*m.LLMData), &v); err != nil {
			t.Fatalf("message %s has invalid llm_data: %v", m.MessageID, err)
		}
		for _, c := range v.Content {
			if c.Type == 5 && c.ToolName != "" { // ContentTypeToolUse
				toolUses++
			}
		}
	}
	if toolUses == 0 {
		t.Fatal("expected tool_use content blocks, found none")
	}

	// Compaction must have advanced the generation and produced compaction
	// summaries + carried-forward messages spanning multiple generations.
	conv, err := database.GetConversationByID(ctx, convID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if conv.CurrentGeneration < 3 {
		t.Errorf("expected current_generation >= 3 after compactions, got %d", conv.CurrentGeneration)
	}

	msgs, err := database.ListMessages(ctx, convID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var distilled, carried, statusMsgs int
	seenGenerations := map[int64]bool{}
	for _, m := range msgs {
		seenGenerations[m.Generation] = true
		if m.UserData == nil {
			continue
		}
		var ud map[string]string
		if err := json.Unmarshal([]byte(*m.UserData), &ud); err != nil {
			continue
		}
		if ud["distilled"] == "true" {
			distilled++
		}
		if ud["compaction_carried"] == "true" {
			carried++
		}
		if ud["distill_status"] != "" {
			statusMsgs++
		}
	}
	if distilled == 0 {
		t.Error("expected compaction summary messages (distilled=true), found none")
	}
	if carried == 0 {
		t.Error("expected carried-forward messages (compaction_carried=true), found none")
	}
	if statusMsgs == 0 {
		t.Error("expected distill status messages, found none")
	}
	if len(seenGenerations) < 3 {
		t.Errorf("expected messages spanning >=3 generations, saw %d", len(seenGenerations))
	}
}

// TestGenerateLoremConversationTimestampsAreChronological verifies a
// message's created_at strictly advances and stays close to the tool/usage
// timestamps embedded in its own llm_data/usage_data (see
// db.CreateMessageParams.CreatedAt for why they must share one timeline). A
// frozen or DB-default created_at fails even at 4 turns.
func TestGenerateLoremConversationTimestampsAreChronological(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	const turns = 4
	convID, err := server.generateLoremConversation(ctx, turns, "claude-opus-4-5")
	if err != nil {
		t.Fatalf("generateLoremConversation: %v", err)
	}

	msgs, err := database.ListMessages(ctx, convID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) < 10 {
		t.Fatalf("expected several messages, got %d", len(msgs))
	}

	// closeBound comfortably covers the generator's measured intra-message
	// spread (~19.2s for this turns count) with no slack for the pre-fix
	// bug (off by minutes) to hide in.
	const closeBound = 25 * time.Second

	var prevCreatedAt time.Time
	checked := 0
	for i, m := range msgs {
		// recordTyped ticks g.clock on every call, so created_at must strictly
		// advance; a frozen/DB-default value would repeat instead.
		if i > 0 && !m.CreatedAt.After(prevCreatedAt) {
			t.Errorf("message %d (seq=%d, type=%s): created_at %s does not strictly advance past previous message's created_at %s",
				i, m.SequenceID, m.Type, m.CreatedAt, prevCreatedAt)
		}
		prevCreatedAt = m.CreatedAt

		assertClose := func(label string, ts time.Time) {
			checked++
			d := ts.Sub(m.CreatedAt)
			if d < 0 {
				d = -d
			}
			if d > closeBound {
				t.Errorf("message %d (seq=%d, type=%s): %s = %s is %s away from created_at = %s (want <= %s)",
					i, m.SequenceID, m.Type, label, ts, d, m.CreatedAt, closeBound)
			}
		}

		if m.UsageData != nil {
			var usage struct {
				StartTime *time.Time `json:"start_time"`
				EndTime   *time.Time `json:"end_time"`
			}
			if err := json.Unmarshal([]byte(*m.UsageData), &usage); err != nil {
				t.Fatalf("message %s has invalid usage_data: %v", m.MessageID, err)
			}
			if usage.StartTime != nil {
				assertClose("usage.start_time", *usage.StartTime)
			}
			if usage.EndTime != nil {
				assertClose("usage.end_time", *usage.EndTime)
			}
		}

		if m.LlmData == nil {
			continue
		}
		var parsed struct {
			Content []struct {
				ToolUseStartTime *time.Time `json:"ToolUseStartTime"`
				ToolUseEndTime   *time.Time `json:"ToolUseEndTime"`
			} `json:"Content"`
		}
		if err := json.Unmarshal([]byte(*m.LlmData), &parsed); err != nil {
			t.Fatalf("message %s has invalid llm_data: %v", m.MessageID, err)
		}
		for _, c := range parsed.Content {
			if c.ToolUseStartTime != nil {
				assertClose("ToolUseStartTime", *c.ToolUseStartTime)
			}
			if c.ToolUseEndTime != nil {
				assertClose("ToolUseEndTime", *c.ToolUseEndTime)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no usage/tool timestamps were found to check; test is not exercising the bug")
	}
}

// TestGenerateLoremConversationPreviewIsReadable verifies that a lorem
// conversation's explicit CreatedAt round-trips through the real
// conversation-list query (ListConversations), whose preview/timestamp
// columns are computed via SQL strftime() (see conversations.sql). strftime()
// can't parse Go's default time.Time string representation, so passing a
// *time.Time straight through as a driver value silently produced an
// unparseable created_at and a blank preview/timestamp for every lorem
// conversation.
func TestGenerateLoremConversationPreviewIsReadable(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	convID, err := server.generateLoremConversation(ctx, 4, "claude-opus-4-5")
	if err != nil {
		t.Fatalf("generateLoremConversation: %v", err)
	}

	items, err := database.ListConversations(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	var item *db.ConversationListItem
	for i := range items {
		if items[i].ConversationID == convID {
			item = &items[i]
			break
		}
	}
	if item == nil {
		t.Fatalf("conversation %s not found in ListConversations", convID)
	}
	if item.PreviewUpdatedAt == "" {
		t.Fatal("PreviewUpdatedAt is blank; strftime() could not parse the message's created_at")
	}
	if _, err := time.Parse(time.RFC3339, item.PreviewUpdatedAt); err != nil {
		t.Errorf("PreviewUpdatedAt %q is not a parseable RFC3339 timestamp: %v", item.PreviewUpdatedAt, err)
	}
	if item.Preview == "" {
		t.Error("Preview is blank")
	}
}

// TestHandleDebugLoremIpsum exercises the HTTP entry point: the GET landing
// page (which must NOT generate) and POST generation, including preset sizes,
// raw counts, and invalid input.
func TestHandleDebugLoremIpsum(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := context.Background()

	// A bare GET must render the landing page and must NOT create anything.
	req := httptest.NewRequest("GET", "/debug/loremipsum", nil)
	w := httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET landing status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET landing content-type = %q, want html", ct)
	}
	if body := w.Body.String(); !strings.Contains(body, "Generate") || !strings.Contains(body, "Custom size") {
		t.Error("GET landing page missing expected controls")
	}
	if convs, err := database.ListConversations(ctx, 10, 0); err != nil {
		t.Fatalf("ListConversations: %v", err)
	} else if len(convs) != 0 {
		t.Fatalf("GET created %d conversations; a GET must have no side effects", len(convs))
	}

	// POST with a preset size and json output returns a conversation id.
	req = httptest.NewRequest("POST", "/debug/loremipsum?json=1", strings.NewReader("size=tiny"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tiny status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		ConversationID string `json:"conversation_id"`
		Turns          int    `json:"turns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ConversationID == "" || out.Turns != 2 {
		t.Fatalf("unexpected response: %+v", out)
	}

	// POST with a raw count redirects to the conversation.
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("raw-count status = %d, want 303", w.Code)
	}

	// Invalid size re-renders the landing page with an error banner (200).
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Invalid size") {
		t.Fatalf("bad size: status=%d, want 200 with banner", w.Code)
	}

	// Over-large size re-renders the landing page with an error banner (200).
	req = httptest.NewRequest("POST", "/debug/loremipsum", strings.NewReader("size=200000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugLoremIpsum(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "too large") {
		t.Fatalf("over-large: status=%d, want 200 with banner", w.Code)
	}
}

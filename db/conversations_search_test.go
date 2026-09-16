package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSearchConversationsFTS(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Active conversation with a message mentioning "pelican"
	active, err := db.CreateConversation(ctx, stringPtr("active-bird"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: active.ConversationID,
		Type:           MessageTypeUser,
		UserData:       map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "I saw a pelican by the bay"}}},
	}); err != nil {
		t.Fatalf("create user msg: %v", err)
	}

	// Archived conversation with a message mentioning "pelican" too
	archived, err := db.CreateConversation(ctx, stringPtr("old-notes"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: archived.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican is a large waterbird"}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}
	if _, err := db.ArchiveConversation(ctx, archived.ConversationID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Decoy conversation with no matching content.
	if _, err := db.CreateConversation(ctx, stringPtr("decoy"), true, nil, nil, ConversationOptions{}); err != nil {
		t.Fatalf("create decoy: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %#v", len(results), results)
	}
	gotActive := false
	gotArchived := false
	for _, r := range results {
		if r.Conversation.ConversationID == active.ConversationID {
			gotActive = true
		}
		if r.Conversation.ConversationID == archived.ConversationID {
			gotArchived = true
		}
		// Every FTS hit should come with a snippet that contains both
		// the matched term and our mark sentinels.
		if r.Snippet == "" {
			t.Errorf("missing snippet for %s", r.Conversation.ConversationID)
		}
		if !strings.Contains(r.Snippet, SnippetMarkStart) || !strings.Contains(r.Snippet, SnippetMarkEnd) {
			t.Errorf("snippet missing mark sentinels: %q", r.Snippet)
		}
		if !strings.Contains(strings.ToLower(r.Snippet), "pelican") {
			t.Errorf("snippet does not contain match term: %q", r.Snippet)
		}
	}
	if !gotActive || !gotArchived {
		t.Errorf("missing expected conversations: active=%v archived=%v", gotActive, gotArchived)
	}

	// Slug match should still work even with no message hits.
	results, err = db.SearchConversationsFTS(ctx, "decoy", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS slug: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 slug result, got %d", len(results))
	}
	// Slug-only hits have no snippet.
	if results[0].Snippet != "" {
		t.Errorf("expected empty snippet for slug-only match, got %q", results[0].Snippet)
	}

	// Prefix matching: typing "peli" should still find the pelican messages.
	results, err = db.SearchConversationsFTS(ctx, "peli", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS prefix: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 prefix results, got %d", len(results))
	}

	// Empty query returns no results without erroring.
	emptyResults, err := db.SearchConversationsFTS(ctx, "   ", 50, 0)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(emptyResults) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(emptyResults))
	}

	// FTS5 syntax characters in user input must not break the query.
	if _, err := db.SearchConversationsFTS(ctx, `pel"ican AND foo*`, 50, 0); err != nil {
		t.Fatalf("escaped query: %v", err)
	}

	// A literal % in the search query must not match every slug via LIKE.
	noise, err := db.SearchConversationsFTS(ctx, "%", 50, 0)
	if err != nil {
		t.Fatalf("percent query: %v", err)
	}
	if len(noise) != 0 {
		t.Errorf("expected 0 results for bare %% query, got %d", len(noise))
	}
}

// TestSearchConversationsFTSStripsCitationMarkers checks the search snippet,
// which is built from raw message JSON and is client-facing.
func TestSearchConversationsFTSStripsCitationMarkers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("cited"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	text := "A pelican is a large waterbird\ue200cite\ue202turn1search0\ue201 that fishes in flocks."
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": text}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	snippet := results[0].Snippet
	if strings.ContainsAny(snippet, "\ue200\ue201\ue202\ue203") {
		t.Errorf("snippet contains citation markers: %q", snippet)
	}
	if strings.Contains(snippet, "citeturn1search0") {
		t.Errorf("snippet contains citation payload: %q", snippet)
	}
	if !strings.Contains(snippet, "that fishes in flocks.") {
		t.Errorf("snippet lost text following the citation: %q", snippet)
	}
}

package db

import (
	"context"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// TestGetLatestActionableMessageIgnoresSlugMarkers guards the retry/continue affordances.
// Five of GetLatestActionableMessage's callers gate on the bottom message being an error
// ("is there something to retry?"). Slug generation runs concurrently with the
// first turn, so its marker can land after an error message; if it counted as
// the latest message, the Retry and Continue buttons would silently stop
// working. Slug markers aren't part of the conversation, so they don't count.
func TestGetLatestActionableMessageIgnoresSlugMarkers(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	conv, err := database.CreateConversation(ctx, stringPtr("latest-vs-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
	}); err != nil {
		t.Fatal(err)
	}
	errMsg, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeError,
		LLMData:        llm.Message{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The slug call finishes after the turn already errored.
	if _, err := database.CreateSlugMessage(ctx, conv.ConversationID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, Model: "m"}},
	}); err != nil {
		t.Fatal(err)
	}

	latest, err := database.GetLatestActionableMessage(ctx, conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.MessageID != errMsg.MessageID {
		t.Errorf("latest message = type %q (seq %d), want the error message: a trailing slug marker must not\n"+
			"hide a retryable error from handleRetry/handleContinue/RetryLastError/ContinueOnModel",
			latest.Type, latest.SequenceID)
	}
}

// TestSlugMarkerDoesNotBreakWarningRun: the consecutive-warning cap stops a
// provider retry storm from filling the DB. "Consecutive" means "after the last
// message of another type", so an invisible slug marker landing mid-storm would
// reset the count and let a fresh batch of warnings through. Slug generation
// races the first turn, so that interleaving happens in practice.
func TestSlugMarkerDoesNotBreakWarningRun(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	conv, err := database.CreateConversation(ctx, stringPtr("warn-vs-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
	}); err != nil {
		t.Fatal(err)
	}

	const maxConsecutive = 3
	// Two warnings, then the slug marker lands, then a third warning.
	for range 2 {
		res, err := database.CreateWarningMessage(ctx, conv.ConversationID, "retrying", maxConsecutive, "suppressing")
		if err != nil {
			t.Fatal(err)
		}
		if res.Suppressed {
			t.Fatal("warning suppressed too early")
		}
	}
	if _, err := database.CreateSlugMessage(ctx, conv.ConversationID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, Model: "m"}},
	}); err != nil {
		t.Fatal(err)
	}

	// The third warning still counts as the third: it must be the last one
	// allowed through (count == maxConsecutive-1 marks it as suppressing).
	third, err := database.CreateWarningMessage(ctx, conv.ConversationID, "retrying", maxConsecutive, "suppressing")
	if err != nil {
		t.Fatal(err)
	}
	if third.Suppressed {
		t.Fatal("third warning suppressed; want it allowed with a suppression notice")
	}
	// The fourth must be suppressed. If the marker had broken the run, the
	// counter would have restarted and this would sail through.
	fourth, err := database.CreateWarningMessage(ctx, conv.ConversationID, "retrying", maxConsecutive, "suppressing")
	if err != nil {
		t.Fatal(err)
	}
	if !fourth.Suppressed {
		t.Error("fourth consecutive warning was not suppressed: a slug marker reset the consecutive-warning counter")
	}
}

// TestListMessagesTailCountsVisibleMessages: ?tail=N is a client's initial
// last-N replay. An invisible slug marker must not consume the window (or
// tail=1 hands back nothing displayable), but a marker that falls inside the
// window must still be returned: clients that detect lost messages by sequence
// contiguity would read a dropped marker as a hole and re-fetch forever.
func TestListMessagesTailCountsVisibleMessages(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	conv, err := database.CreateConversation(ctx, stringPtr("tail-vs-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        llm.Message{Role: llm.MessageRoleAssistant},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Marker physically last, as happens when slug generation finishes after
	// the first turn.
	if _, err := database.CreateSlugMessage(ctx, conv.ConversationID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, Model: "m"}},
	}); err != nil {
		t.Fatal(err)
	}

	var tail []generated.Message
	if err := database.Queries(ctx, func(q *generated.Queries) error {
		var err error
		tail, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{
			ConversationID: conv.ConversationID,
			Limit:          1,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// tail=1 must reach back far enough to include the agent message; the
	// marker rides along rather than being dropped from the middle.
	if len(tail) != 2 || tail[0].MessageID != agent.MessageID || tail[1].Type != string(MessageTypeSlug) {
		t.Fatalf("tail=1 returned %+v, want the agent message (plus the trailing marker): "+
			"an invisible slug marker must not consume the window", tail)
	}
	if tail[1].SequenceID != tail[0].SequenceID+1 {
		t.Errorf("tail rows are not contiguous (%d then %d); a hole reads as lost data to "+
			"contiguity-based clients", tail[0].SequenceID, tail[1].SequenceID)
	}

	// A marker deeper in the history must not shrink the visible window either.
	user, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tail2 []generated.Message
	if err := database.Queries(ctx, func(q *generated.Queries) error {
		var err error
		tail2, err = q.ListMessagesTail(ctx, generated.ListMessagesTailParams{
			ConversationID: conv.ConversationID,
			Limit:          2,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Visible tail of 2 = agent + user, with the marker between them.
	if len(tail2) != 3 || tail2[0].MessageID != agent.MessageID || tail2[2].MessageID != user.MessageID {
		t.Errorf("tail=2 returned %+v, want agent + marker + user", tail2)
	}
}

// TestForkGenerationIgnoresSlugMarker: fork copies the generation that was
// active at the fork point, resolved by GetGenerationAtOrBeforeSequence. A slug
// marker is stamped with whatever current_generation holds when the racing slug
// goroutine lands (15s timeout), so a marker written after a compaction bumped
// the generation would answer that question with the NEW generation and make the
// fork copy the wrong set of messages.
func TestForkGenerationIgnoresSlugMarker(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	conv, err := database.CreateConversation(ctx, stringPtr("fork-gen-vs-slug"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Generation 1: the real history a fork should copy.
	gen1, err := database.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A compaction bumps the generation...
	if err := database.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.IncrementConversationGeneration(ctx, conv.ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// ...and only THEN does the in-flight slug goroutine land, stamping its
	// marker with generation 2.
	marker, err := database.CreateSlugMessage(ctx, conv.ConversationID, []llm.PurposedUsage{
		{Purpose: "slug", Usage: llm.Usage{InputTokens: 10, Model: "m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if marker.Generation == gen1.Generation {
		t.Fatalf("setup: marker generation %d should differ from %d", marker.Generation, gen1.Generation)
	}

	// Forking at the marker's sequence_id must still copy generation 1.
	forked, err := database.ForkConversation(ctx, conv.ConversationID, marker.SequenceID)
	if err != nil {
		t.Fatal(err)
	}
	forkedMsgs, err := database.ListMessages(ctx, forked.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forkedMsgs) != 1 || forkedMsgs[0].ForkedFromMessageID == nil || *forkedMsgs[0].ForkedFromMessageID != gen1.MessageID {
		t.Errorf("forked messages = %+v, want the generation-1 user message; a slug marker "+
			"must not decide which generation the fork copies", forkedMsgs)
	}
}

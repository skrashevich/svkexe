package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestConversationListPatchNewConversationEmitsFieldAdd is the server-side half
// of a cross-language contract guard. It runs the most ordinary scenario there
// is — create a brand-new conversation and let the (predictable) agent answer
// its first message — and asserts that the /api/stream2 conversation-list patch
// stream emits an RFC 6902 `add` op at a FIELD path (e.g. `/0/preview`), not
// just whole-row adds or `replace`s.
//
// Why this matters: `omitempty` fields (preview, preview_updated_at, slug,
// max_sequence_id...) are absent from a freshly-added row, so the first time
// they become set the diff generator (fieldDiffOps in
// conversation_list_patch_diff.go) MUST use `add` — `replace` would violate
// RFC 6902 (target must exist). A field-path `add` is therefore an unavoidable,
// routine part of the wire contract.
//
// The iOS client (ConversationListPatcher.applyOne) once handled `add` only at
// an array index (`/N`) and threw on a field-path `add` (`/N/preview`), which
// forced a full stream2 reconnect and dropped the in-flight reply ("new
// conversation's reply flashes in then disappears"). The Swift regression test
// `testNewConversationFirstMessageNeverReconnects` in StreamCoordinatorTests
// replays this same frame shape and asserts no reconnect. This Go test guards
// the other side: that the server really does produce the `add /N/<field>`
// shape, so the contract the Swift test encodes can't silently drift.
func TestConversationListPatchNewConversationEmitsFieldAdd(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newFlusherRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { server.handleStream(rec, req); close(done) }()
	defer func() { cancel(); <-done }()

	initial := waitForPatchEventAfter(t, rec, "")
	if !initial.Reset {
		t.Fatalf("expected initial reset, got %+v", initial)
	}

	// Create a new conversation whose first message gets a deterministic reply.
	body, _ := json.Marshal(ChatRequest{Message: "echo: lemon", Model: "predictable"})
	w := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, "/api/conversations/new", strings.NewReader(string(body)))
	cr.Header.Set("Content-Type", "application/json")
	server.handleNewConversation(w, cr)
	if w.Code != http.StatusCreated {
		t.Fatalf("new conversation: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Walk the patch stream forward, applying each patch onto a local mirror so
	// every op is exercised (this also proves the frames are self-consistent),
	// until we observe a field-path `add` op or time out.
	state := mustApplyPatch(t, []ConversationWithState{}, initial.Patch)
	deadline := time.Now().Add(5 * time.Second)
	prev := initial.NewHash
	sawFieldAdd := false
	var fieldAddPath string
	var seen []string
	for time.Now().Before(deadline) && !sawFieldAdd {
		ev := nextPatchAfter(rec, prev)
		if ev == nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		prev = ev.NewHash
		state = mustApplyPatch(t, state, ev.Patch)
		verifyHash(t, state, ev.NewHash)
		for _, op := range ev.Patch {
			seen = append(seen, op.Op+" "+op.Path)
			// A field-path add: path is `/<index>/<field>` (two segments),
			// op is "add". This is the shape that used to crash the client.
			if op.Op == "add" && isFieldPath(op.Path) {
				sawFieldAdd = true
				fieldAddPath = op.Path
			}
		}
	}

	if !sawFieldAdd {
		t.Fatalf("never observed a field-path `add` op for a new conversation's first message.\nops seen:\n  %s",
			strings.Join(seen, "\n  "))
	}
	t.Logf("observed field-path add: %s (ops: %s)", fieldAddPath, strings.Join(seen, ", "))
}

// isFieldPath reports whether a JSON Pointer is `/<index>/<field>` (a field
// within a row) rather than `/<index>` (a whole row).
func isFieldPath(path string) bool {
	return strings.Count(strings.TrimPrefix(path, "/"), "/") == 1
}

// nextPatchAfter returns the first not-yet-consumed patch event chained to
// prevHash (oldHash == prevHash), or nil if none has arrived yet.
func nextPatchAfter(rec *flusherRecorder, prevHash string) *ConversationListPatchEvent {
	for _, ev := range parseConversationStreamListPatches(rec.getString()) {
		if ev.OldHash != nil && *ev.OldHash == prevHash {
			e := ev
			return &e
		}
	}
	return nil
}

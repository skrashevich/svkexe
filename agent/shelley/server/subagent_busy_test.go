package server

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// These tests cover sending a message to a subagent whose current turn is
// still in flight. The old behavior cancelled that turn (discarding its work
// and mis-reporting the cancellation to the parent as a completion). The new
// behavior never interrupts: wait=false queues the message; wait=true waits
// for the current turn to finish before sending the follow-up.

func TestSubagentBusy(t *testing.T) {
	t.Run("WaitFalse_QueuesInsteadOfCancelling", testSubagentBusy_WaitFalseQueues)
	t.Run("WaitTrue_WaitsForIdleThenSends_NoCancel", testSubagentBusy_WaitTrueWaitsForIdle)
	t.Run("WaitTrue_DeadlineDuringInFlightTurn_ReturnsProgress", testSubagentBusy_WaitTrueDeadline)
	t.Run("WaitTrue_InFlightFinishThenFollowupTimeout_NoPrematureNotify", testSubagentBusy_InFlightFinishThenFollowupTimeout)
}

// A reasoning level passed to RunSubagent must be persisted on the subagent's
// conversation options so the loop picks it up. An empty reasoning string is a
// no-op and must not overwrite an existing level.
func TestSubagentRunner_PersistsReasoning(t *testing.T) {
	f := newSubagentDoneFixture(t, "irrelevant")
	ctx := context.Background()

	runner := NewSubagentRunner(f.server)
	// Send with an explicit reasoning level; wait=false returns immediately.
	if _, err := runner.RunSubagent(ctx, f.subagentID, "do it", false, time.Minute, "predictable", "high"); err != nil {
		t.Fatalf("RunSubagent(reasoning=high): %v", err)
	}

	var opts string
	if err := f.database.Queries(ctx, func(q *generated.Queries) error {
		var e error
		opts, e = q.GetConversationOptions(ctx, f.subagentID)
		return e
	}); err != nil {
		t.Fatalf("get conversation options: %v", err)
	}
	if got := db.ParseConversationOptions(opts).ThinkingLevel; got != "high" {
		t.Fatalf("expected persisted thinking_level 'high', got %q", got)
	}

	// A subsequent empty reasoning must not clobber the stored level.
	if _, err := runner.RunSubagent(ctx, f.subagentID, "again", false, time.Minute, "predictable", ""); err != nil {
		t.Fatalf("RunSubagent(reasoning=\"\"): %v", err)
	}
	if err := f.database.Queries(ctx, func(q *generated.Queries) error {
		var e error
		opts, e = q.GetConversationOptions(ctx, f.subagentID)
		return e
	}); err != nil {
		t.Fatalf("get conversation options: %v", err)
	}
	if got := db.ParseConversationOptions(opts).ThinkingLevel; got != "high" {
		t.Fatalf("empty reasoning clobbered level; got %q", got)
	}
}

// pendingBatchCount returns the number of queued pending batches (test-only,
// reads under the manager lock).
func pendingBatchCount(cm *ConversationManager) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return len(cm.pendingBatches)
}

// hasCancelledMessage reports whether the subagent recorded an
// "[Operation cancelled]" end-of-turn message (the cancel-path artifact we
// want to be sure no longer appears on the resend path).
func hasCancelledMessage(t *testing.T, f *subagentDoneFixture) bool {
	t.Helper()
	msgs, err := f.database.ListMessages(context.Background(), f.subagentID)
	if err != nil {
		t.Fatalf("list subagent messages: %v", err)
	}
	for _, m := range msgs {
		if m.LlmData != nil && strings.Contains(*m.LlmData, "[Operation cancelled]") {
			return true
		}
	}
	return false
}

// wait=false to a busy subagent must queue the message (not cancel the turn)
// and return immediately with a "queued" status.
func testSubagentBusy_WaitFalseQueues(t *testing.T) {
	f := newSubagentDoneFixture(t, "irrelevant")

	// Bring the subagent's loop up and mark it working to simulate an
	// in-flight turn.
	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	f.subagentMgr.SetAgentWorking(true)

	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "do this next", false, time.Minute, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=false): %v", err)
	}
	if !strings.Contains(res, "queued") {
		t.Fatalf("expected a queued status, got %q", res)
	}
	if hasCancelledMessage(t, f) {
		t.Fatalf("subagent turn was cancelled; expected it to keep running")
	}
	if n := pendingBatchCount(f.subagentMgr); n != 1 {
		t.Fatalf("expected exactly one queued batch, got %d", n)
	}
}

// wait=true to a busy subagent must wait for the current turn to finish
// (without cancelling it) and then send the follow-up, returning the
// follow-up's response.
func testSubagentBusy_WaitTrueWaitsForIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSubagentDoneFixture(t, "irrelevant")
		defer stopActiveConversationLoops(f.server)

		if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
			t.Fatalf("ensureLoop subagent: %v", err)
		}
		// Simulate an in-flight turn that finishes shortly.
		f.subagentMgr.SetAgentWorking(true)
		go func() {
			time.Sleep(300 * time.Millisecond)
			f.subagentMgr.SetAgentWorking(false)
		}()

		parentBefore := countSyntheticDonePairs(t, f.parentMessages())

		runner := NewSubagentRunner(f.server)
		res, err := runner.RunSubagent(context.Background(), f.subagentID, "echo: foo", true, 10*time.Second, "predictable", "")
		if err != nil {
			t.Fatalf("RunSubagent(wait=true): %v", err)
		}
		if hasCancelledMessage(t, f) {
			t.Fatalf("subagent turn was cancelled; expected it to finish on its own")
		}
		// The follow-up ("echo: foo") drives the predictable model to reply "foo".
		if !strings.Contains(res, "foo") {
			t.Fatalf("expected the follow-up response 'foo', got %q", res)
		}
		// The in-flight turn's finish happened while our waiter slot was held, so
		// it must NOT have produced a spurious async completion on the parent; we
		// delivered synchronously instead.
		if n := countSyntheticDonePairs(t, f.parentMessages()); n != parentBefore {
			t.Fatalf("expected no new synthetic done pairs on parent, got %d (was %d)", n, parentBefore)
		}
	})
}

// If the deadline passes while the subagent's current turn is still running,
// the wait returns a progress summary and does not cancel the turn.
func testSubagentBusy_WaitTrueDeadline(t *testing.T) {
	f := newSubagentDoneFixture(t, "irrelevant")

	if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
		t.Fatalf("ensureLoop subagent: %v", err)
	}
	// Stays working past the (tiny) deadline.
	f.subagentMgr.SetAgentWorking(true)

	runner := NewSubagentRunner(f.server)
	res, err := runner.RunSubagent(context.Background(), f.subagentID, "echo: foo", true, 300*time.Millisecond, "predictable", "")
	if err != nil {
		t.Fatalf("RunSubagent(wait=true) deadline: %v", err)
	}
	if !strings.Contains(res, "still working") {
		t.Fatalf("expected a progress summary on deadline, got %q", res)
	}
	if hasCancelledMessage(t, f) {
		t.Fatalf("subagent turn was cancelled on deadline; expected it to keep running")
	}
	// We never sent the follow-up (the turn never ended), so it must not be
	// queued either — it is simply dropped, and the parent can re-ask.
	if n := pendingBatchCount(f.subagentMgr); n != 0 {
		t.Fatalf("expected no queued batch on deadline, got %d", n)
	}

	// Clean up the lingering working state so the fixture teardown is quiet.
	f.subagentMgr.SetAgentWorking(false)
}

// Regression for a cross-turn leak: the in-flight turn finishes while we wait
// to send (recording a suppressed finish), then OUR follow-up turn times out.
// The stale suppressed-finish from the earlier turn must NOT be misattributed
// to the follow-up and fire a premature notification. Exactly one async
// completion fires, and only once the follow-up actually finishes.
func testSubagentBusy_InFlightFinishThenFollowupTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSubagentDoneFixture(t, "irrelevant")
		defer stopActiveConversationLoops(f.server)

		if err := f.subagentMgr.ensureLoop(f.llmSvc, "predictable"); err != nil {
			t.Fatalf("ensureLoop subagent: %v", err)
		}

		// In-flight turn finishes shortly (this is the finish we wait out; it
		// records a suppressed-finish because our waiter slot is held).
		f.subagentMgr.SetAgentWorking(true)
		go func() {
			time.Sleep(200 * time.Millisecond)
			f.subagentMgr.SetAgentWorking(false)
		}()

		// Make the follow-up turn slow so OUR wait times out before it finishes.
		f.llmSvc.SetResponseDelay(2 * time.Second)

		runner := NewSubagentRunner(f.server)
		res, err := runner.RunSubagent(context.Background(), f.subagentID, "echo: foo", true, 700*time.Millisecond, "predictable", "")
		if err != nil {
			t.Fatalf("RunSubagent(wait=true): %v", err)
		}
		if !strings.Contains(res, "still working") {
			t.Fatalf("expected a progress summary (follow-up timed out), got %q", res)
		}

		// The earlier in-flight finish (which we waited out) must NOT count as an
		// undelivered finish of our follow-up turn. Over the whole lifecycle the
		// count must converge to exactly ONE async completion — fired when the
		// follow-up actually finishes — and never exceed it. Without
		// consumeSuppressedFinish, the timeout's endWait fires one notification
		// (mis-attributing the stale suppressed finish) AND the follow-up's real
		// finish fires a second, yielding two.
		waitFor(t, 6*time.Second, func() bool {
			return countSyntheticDonePairs(t, f.parentMessages()) == 1
		})
		// Run the clock well past every pending timer, then confirm nothing
		// else fired a second pair.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if n := countSyntheticDonePairs(t, f.parentMessages()); n != 1 {
			t.Fatalf("expected exactly one synthetic done pair, got %d", n)
		}
	})
}

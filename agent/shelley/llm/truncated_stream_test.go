package llm

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestTruncatedStreamIsRetryable(t *testing.T) {
	err := TruncatedStream("incomplete chat completion stream: no finish reason", nil)
	info, ok := RequestErrorInfoFromError(err)
	if !ok {
		t.Fatalf("RequestErrorInfoFromError(%v) reported no metadata", err)
	}
	if !info.Retryable {
		t.Errorf("Retryable = false, want true for a truncated stream")
	}
}

func TestTruncatedStreamKeepsProviderWording(t *testing.T) {
	const detail = "incomplete stream: no stop_reason received (stream may have been truncated)"
	if got := TruncatedStream(detail, nil).Error(); got != detail {
		t.Errorf("Error() = %q, want %q", got, detail)
	}
}

func TestTruncatedStreamUnwrapsCause(t *testing.T) {
	err := TruncatedStream("chat completion stream failed after response started", io.ErrUnexpectedEOF)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(%v, io.ErrUnexpectedEOF) = false, want true", err)
	}
	want := "chat completion stream failed after response started: unexpected EOF"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A truncated stream stays retryable after it is wrapped and joined the way the
// providers report a failed attempt, because that is how the loop's classifiers
// actually receive it.
func TestTruncatedStreamSurvivesWrapping(t *testing.T) {
	inner := TruncatedStream("incomplete chat completion stream: no finish reason", nil)
	wrapped := errors.Join(
		errors.New("attempt 1 at 2026-09-10 20:09:03"),
		errors.New("url=https://openrouter.ai/api/v1/chat/completions model=openrouter/free: "+inner.Error()),
	)
	if _, ok := RequestErrorInfoFromError(wrapped); ok {
		t.Fatal("plain text copies must not report metadata; the test would prove nothing")
	}
	joined := errors.Join(errors.New("attempt 1"), inner)
	info, ok := RequestErrorInfoFromError(joined)
	if !ok || !info.Retryable {
		t.Errorf("RequestErrorInfoFromError(joined) = %+v, %v; want retryable metadata", info, ok)
	}
}

// A cut stream on one attempt must not lend its verdict to the attempt that
// actually ended the request: the providers hand back every attempt joined
// together, and answering with the first one turns a permanent failure into a
// request that is repeated until the retry budget runs out.
func TestRequestErrorInfoAnswersForTheFinalAttempt(t *testing.T) {
	truncated := TruncatedStream("incomplete chat completion stream: no finish reason", nil)
	permanent := errors.Join(
		fmt.Errorf("attempt 1 at 2026-09-10 20:09:03: %w", truncated),
		errors.New("attempt 2 at 2026-09-10 20:09:20: status 401 (url=U, model=M): invalid api key"),
	)
	if info, ok := RequestErrorInfoFromError(permanent); ok {
		t.Errorf("RequestErrorInfoFromError = %+v, true; want the 401 attempt to carry no metadata", info)
	}
	if got := FinalAttempt(permanent).Error(); !strings.Contains(got, "invalid api key") {
		t.Errorf("FinalAttempt = %q, want the attempt the request ended on", got)
	}

	// The mirror case: the last attempt is the truncated one, so the metadata
	// does apply.
	transient := errors.Join(
		errors.New("attempt 1 at 2026-09-10 20:09:03: status 500"),
		fmt.Errorf("attempt 2 at 2026-09-10 20:09:20: %w", truncated),
	)
	info, ok := RequestErrorInfoFromError(transient)
	if !ok || !info.Retryable {
		t.Errorf("RequestErrorInfoFromError = %+v, %v; want the truncated final attempt to be retryable", info, ok)
	}
}

// A provider that gives up wraps the accumulated attempts behind one closing
// message. The verdict still belongs to the attempt inside, not to the first one
// the wrapper happens to reach.
func TestFinalAttemptSeesThroughAClosingWrapper(t *testing.T) {
	truncated := TruncatedStream("incomplete chat completion stream: no finish reason", nil)
	wrapped := fmt.Errorf("openai request failed after 16 attempts: %w", errors.Join(
		fmt.Errorf("attempt 1: %w", truncated),
		errors.New("attempt 2: status 401 (url=U, model=M): invalid api key"),
	))
	if info, ok := RequestErrorInfoFromError(wrapped); ok {
		t.Errorf("RequestErrorInfoFromError = %+v, true; want the 401 attempt to carry no metadata", info)
	}
	if got := FinalAttempt(wrapped).Error(); !strings.Contains(got, "invalid api key") {
		t.Errorf("FinalAttempt = %q, want the attempt the request ended on", got)
	}
}

// A closing message wrapped more than once still resolves to the attempts
// underneath it, or the fix would only hold for the depth it was written at.
func TestFinalAttemptSeesThroughNestedWrappers(t *testing.T) {
	wrapped := fmt.Errorf("LLM request failed: %w", fmt.Errorf("openai request failed after 16 attempts: %w", errors.Join(
		fmt.Errorf("attempt 1: %w", TruncatedStream("incomplete chat completion stream: no finish reason", nil)),
		errors.New("attempt 2: status 401 (url=U, model=M): invalid api key"),
	)))
	if info, ok := RequestErrorInfoFromError(wrapped); ok {
		t.Errorf("RequestErrorInfoFromError = %+v, true; want the 401 attempt to carry no metadata", info)
	}
	if got := FinalAttempt(wrapped).Error(); !strings.Contains(got, "invalid api key") {
		t.Errorf("FinalAttempt = %q, want the attempt the request ended on", got)
	}
}

// A wrapper that hides no join is itself the final error: stepping through it
// would drop the message the provider ended on.
func TestFinalAttemptKeepsAPlainWrapper(t *testing.T) {
	wrapped := fmt.Errorf("openai request failed: %w", TruncatedStream("incomplete chat completion stream: no finish reason", nil))
	if got := FinalAttempt(wrapped).Error(); got != wrapped.Error() {
		t.Errorf("FinalAttempt = %q, want the wrapper itself %q", got, wrapped.Error())
	}
	// The metadata is still found, because errors.As walks the wrapper.
	info, ok := RequestErrorInfoFromError(wrapped)
	if !ok || !info.Retryable {
		t.Errorf("RequestErrorInfoFromError = %+v, %v; want the truncated stream to stay retryable", info, ok)
	}
}

// A single error is its own final attempt, and a nil error has none.
func TestFinalAttemptLeavesUnjoinedErrorsAlone(t *testing.T) {
	plain := errors.New("just the one")
	if got := FinalAttempt(plain); got != plain {
		t.Errorf("FinalAttempt(%v) = %v, want the error itself", plain, got)
	}
	if got := FinalAttempt(nil); got != nil {
		t.Errorf("FinalAttempt(nil) = %v, want nil", got)
	}
}

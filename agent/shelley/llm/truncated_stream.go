package llm

import "fmt"

// truncatedStreamError marks a provider stream that ended before the provider
// said why it stopped. It is a transport failure wearing a protocol-shaped
// message: the socket died, or a proxy cut the response, and the last chunk
// carrying the stop reason never arrived.
//
// Re-sending the same request is safe. A truncated stream yields no response at
// all, so no tool ran and no assistant message was recorded; the retry starts
// from exactly the conversation state the failed attempt started from.
type truncatedStreamError struct {
	detail string
	cause  error
}

var _ RequestError = (*truncatedStreamError)(nil)

// TruncatedStream wraps a truncated-stream failure so the retry classifiers see
// it as the transient failure it is instead of ending the turn for good. detail
// keeps the provider's own wording, so existing error text is unchanged; cause
// may be nil when the stream simply ran out.
func TruncatedStream(detail string, cause error) error {
	return &truncatedStreamError{detail: detail, cause: cause}
}

func (e *truncatedStreamError) Error() string {
	if e.cause == nil {
		return e.detail
	}
	return fmt.Sprintf("%s: %v", e.detail, e.cause)
}

func (e *truncatedStreamError) Unwrap() error { return e.cause }

func (e *truncatedStreamError) RequestErrorInfo() RequestErrorInfo {
	return RequestErrorInfo{Retryable: true}
}

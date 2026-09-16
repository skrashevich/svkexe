package llm

import (
	"errors"
	"time"
)

// RequestErrorInfo describes a provider-neutral LLM request failure.
type RequestErrorInfo struct {
	// Retryable reports whether immediately repeating the request is safe.
	Retryable bool
	// IdleStallDuration is the no-progress window before an idle abort.
	IdleStallDuration time.Duration
}

// RequestError exposes provider-neutral LLM request failure metadata.
type RequestError interface {
	error
	RequestErrorInfo() RequestErrorInfo
}

// RequestErrorInfoFromError finds request failure metadata in err.
func RequestErrorInfoFromError(err error) (RequestErrorInfo, bool) {
	var requestErr RequestError
	if !errors.As(err, &requestErr) {
		return RequestErrorInfo{}, false
	}
	return requestErr.RequestErrorInfo(), true
}

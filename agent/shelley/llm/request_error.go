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
//
// Providers accumulate one wrapped error per attempt with errors.Join and
// return the whole pile, so a plain errors.As would answer with the metadata of
// whichever attempt happens to come first — a cut stream on attempt 1 would make
// a permanent "invalid api key" on attempt 2 look retryable, and the request
// would be repeated until the budget ran out. Only the last branch of a join
// describes how the request actually ended, so that is the one asked.
func RequestErrorInfoFromError(err error) (RequestErrorInfo, bool) {
	err = FinalAttempt(err)
	var requestErr RequestError
	if !errors.As(err, &requestErr) {
		return RequestErrorInfo{}, false
	}
	return requestErr.RequestErrorInfo(), true
}

// FinalAttempt walks past the accumulated earlier attempts of a joined error to
// the one that ended the request. A single error is returned unchanged.
//
// Classifying a request means answering "how did it end", so anything deciding
// whether to repeat a request should look here rather than at the joined text,
// which still describes every attempt that came before.
func FinalAttempt(err error) error {
	for {
		switch chain := err.(type) {
		case interface{ Unwrap() []error }:
			attempts := chain.Unwrap()
			if len(attempts) == 0 {
				return err
			}
			err = attempts[len(attempts)-1]
		case interface{ Unwrap() error }:
			// A single wrapper is stepped through only when it hides a join:
			// providers that give up hand back the accumulated attempts behind
			// one closing message, and the attempt that ended the request is
			// inside. A wrapper around anything else already is the final error,
			// message and all, and unwrapping it would throw that message away.
			inner := joinUnder(err)
			if inner == nil {
				return err
			}
			err = inner
		default:
			return err
		}
	}
}

// joinUnder returns the joined error a chain of single wrappers leads to, or nil
// when it leads anywhere else. The wrappers are followed to the end rather than
// one at a time, so a closing message wrapped twice still resolves to the
// attempts underneath it.
func joinUnder(err error) error {
	for {
		wrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		inner := wrapper.Unwrap()
		if inner == nil {
			return nil
		}
		if _, joined := inner.(interface{ Unwrap() []error }); joined {
			return inner
		}
		err = inner
	}
}

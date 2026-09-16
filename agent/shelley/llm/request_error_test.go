package llm

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type testRequestError struct {
	info RequestErrorInfo
}

func (e *testRequestError) Error() string { return "request failed" }

func (e *testRequestError) RequestErrorInfo() RequestErrorInfo { return e.info }

func TestRequestErrorInfoFromError(t *testing.T) {
	want := RequestErrorInfo{Retryable: true, IdleStallDuration: 3 * time.Minute}
	err := errors.Join(errors.New("earlier attempt"), fmt.Errorf("latest attempt: %w", &testRequestError{info: want}))
	got, ok := RequestErrorInfoFromError(err)
	if !ok || got != want {
		t.Fatalf("RequestErrorInfoFromError = %+v, %v; want %+v, true", got, ok, want)
	}
}

func TestRequestErrorInfoFromErrorWithoutMetadata(t *testing.T) {
	got, ok := RequestErrorInfoFromError(errors.New("request failed"))
	if ok || got != (RequestErrorInfo{}) {
		t.Fatalf("RequestErrorInfoFromError = %+v, %v; want zero value, false", got, ok)
	}
}

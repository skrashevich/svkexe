package oai

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// A queued model keeps its connection alive with comments for longer than
// go-openai's 300-empty-message ceiling. Before those comments were stripped,
// the stream died with "stream has sent too many empty messages" and took the
// user's turn with it.
func TestKeepAliveCommentsDoNotEndTheStream(t *testing.T) {
	var body bytes.Buffer
	for range 500 {
		body.WriteString(": OPENROUTER PROCESSING\n\n")
	}
	body.WriteString("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	body.WriteString("data: [DONE]\n\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body.String())
	}))
	defer srv.Close()

	resp, err := withoutSSEComments(srv.Client()).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "OPENROUTER PROCESSING") {
		t.Error("keep-alive comments reached the parser")
	}
	if !strings.Contains(string(got), `"content":"hi"`) {
		t.Errorf("the model's own output was lost: %q", got)
	}
}

// Whatever the provider sends must reach the caller as it arrives: buffering
// the stream would turn a live answer into one delayed block.
func TestDataFramesArriveBeforeTheStreamEnds(t *testing.T) {
	pr, pw := io.Pipe()
	stripped := stripSSEComments(pr)
	defer stripped.Close()

	go func() {
		fmt.Fprint(pw, ": OPENROUTER PROCESSING\n")
		fmt.Fprint(pw, "data: first\n")
		// The response is deliberately left open: a real stream is still
		// running when its first frames are read.
	}()

	lines := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(stripped).ReadString('\n')
		if err != nil {
			return
		}
		lines <- line
	}()

	select {
	case line := <-lines:
		if line != "data: first\n" {
			t.Errorf("got %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame delivered while the stream was still open")
	}
}

// The whole point, end to end: a turn survives a provider that spends a long
// queue wait sending nothing but keep-alives.
func TestServiceDoSurvivesLongKeepAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 500 {
			fmt.Fprint(w, ": OPENROUTER PROCESSING\n\n")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	svc := &Service{APIKey: "test-key", Model: modelForTest("test"), ModelURL: server.URL, Backoff: []time.Duration{0}}
	resp, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser}}, OnStream: func(llm.StreamDelta) {}})
	if err != nil {
		t.Fatalf("Do() error = %v, want the answer that followed the keep-alives", err)
	}
	if len(resp.Content) == 0 || !strings.Contains(resp.Content[0].Text, "answer") {
		t.Fatalf("content = %+v", resp.Content)
	}
}

// A non-event-stream response must be handed back untouched, including bodies
// that happen to start with a colon.
func TestNonStreamResponsesAreUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ":not a comment\n")
	}))
	defer srv.Close()

	resp, err := withoutSSEComments(srv.Client()).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ":not a comment\n" {
		t.Errorf("body altered: %q", got)
	}
}

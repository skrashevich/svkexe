package llmproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestPicoClawSurvivesATruncatedStream is the regression test for the failure
// that ended a real install task halfway through: the provider's SSE stream was
// cut before any finish_reason arrived, the agent called that permanent, the
// turn ended after one attempt, and the Retry button was disabled — so the work
// already done was stranded with no way to continue it.
//
// A truncated stream is a transport hiccup. The agent must retry it on its own,
// and when its own attempts run out the failure must stay retryable, so the
// gateway's task monitor can ask for the conversation to be picked back up.
//
// Run with SVKEXE_TEST_AGENT_BINARY pointing at a native build.
func TestPicoClawSurvivesATruncatedStream(t *testing.T) {
	binary := os.Getenv("SVKEXE_TEST_AGENT_BINARY")
	if binary == "" {
		t.Skip("set SVKEXE_TEST_AGENT_BINARY to the native agent build")
	}
	work := t.TempDir()

	// truncations is how many of the agent's turns are still to be cut short.
	// The agent retries a failed request twice on its own, so cutting three
	// streams forces it all the way to a recorded error and leaves the fourth
	// attempt — the one the resume makes — free to succeed.
	var truncations atomic.Int32
	truncations.Store(3)
	var turns atomic.Int32

	gateway := New(Config{APIKey: "provider-key", InternalToken: "agent-token", Models: []string{"test/model"}})
	gateway.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Stream bool              `json:"stream"`
			Tools  []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		// Only the turns that carry tools are the agent's actual work; slug
		// generation and other side requests must answer normally so the test
		// measures the turn it means to.
		if !req.Stream || len(req.Tools) == 0 {
			return jsonCompletion("side-request-reply"), nil
		}
		turns.Add(1)
		if truncations.Add(-1) >= 0 {
			return truncatedStreamResponse(), nil
		}
		return completedStreamResponse(resumedAnswer), nil
	})

	mux := http.NewServeMux()
	mux.Handle("POST /api/llm/v1/chat/completions", gateway)
	mux.HandleFunc("GET /api/llm/v1/models", gateway.ServeModels)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()

	agent := startAgent(t, binary, work)
	defer agent.stop()

	var model struct {
		ID string `json:"model_id"`
	}
	status, data := agent.call(t, "POST", "/api/custom-models", map[string]any{
		"display_name": "svkexe truncated stream", "provider_type": "openai",
		"endpoint": proxy.URL + "/api/llm/v1", "api_key": "agent-token",
		"model_name": "test/model", "max_tokens": 8192,
	})
	if status != 200 && status != 201 {
		t.Fatalf("create model: %d %s", status, data)
	}
	json.Unmarshal(data, &model)

	var conv struct {
		ID string `json:"conversation_id"`
	}
	status, data = agent.call(t, "POST", "/api/conversations/new", map[string]string{
		"message": "Say the magic word.", "model": model.ID, "cwd": work,
	})
	if status != 200 && status != 201 {
		t.Fatalf("create conversation: %d %s", status, data)
	}
	json.Unmarshal(data, &conv)

	// The agent's own retries are spent on the cut streams, so the turn ends on
	// a recorded error. That error is what the owner and the gateway see.
	history := agent.await(t, conv.ID, func(body string) bool {
		return strings.Contains(body, `"type":"error"`)
	}, "an error message for the truncated stream")
	if !strings.Contains(history, "incomplete chat completion stream") {
		t.Fatalf("the failure was not reported as a truncated stream: %s", history)
	}
	if got := turns.Load(); got < 2 {
		t.Fatalf("the agent made %d attempts, want it to retry a truncated stream on its own", got)
	}
	// The answer must not be reachable yet, or the assertion below would prove
	// nothing about the resume.
	if strings.Contains(history, resumedAnswer) {
		t.Fatalf("the answer is already in the conversation before the resume: %s", history)
	}
	turnsBeforeResume := turns.Load()

	// Before the fix this answered 409 "error is not retryable", which is what
	// left the task with nothing to continue it.
	status, data = agent.call(t, "POST", "/api/conversation/"+conv.ID+"/retry", nil)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("resume refused: %d %s", status, data)
	}
	// A 2xx alone is not enough: the agent answers 202 "not_applicable" without
	// running anything.
	if strings.Contains(string(data), "not_applicable") {
		t.Fatalf("the agent accepted the resume without running it: %s", data)
	}

	history = agent.await(t, conv.ID, func(body string) bool {
		return strings.Contains(body, resumedAnswer)
	}, "the answer the resumed turn produced")
	if got := turns.Load(); got <= turnsBeforeResume {
		t.Fatalf("the upstream saw %d turns after the resume and %d before; the resume never ran", got, turnsBeforeResume)
	}
	// The answer has to come after the failure, not from anywhere earlier in the
	// log: the job was finished, not merely mentioned.
	if strings.Index(history, resumedAnswer) < strings.Index(history, `"type":"error"`) {
		t.Fatalf("the answer does not follow the failure it was supposed to recover from: %s", history)
	}
}

// resumedAnswer is deliberately absent from the prompt and from every side
// request, so finding it in the conversation can only mean the resumed turn
// produced it.
const resumedAnswer = "resumed-answer-9f3a"

// truncatedStreamResponse is a stream that stops mid-answer: chunks arrive, then
// the body ends without a finish_reason and without [DONE].
func truncatedStreamResponse() *http.Response {
	chunk := map[string]any{
		"id": "completion-cut", "object": "chat.completion.chunk", "model": "test/model",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "half an ans"}, "finish_reason": nil}},
	}
	data, _ := json.Marshal(chunk)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + string(data) + "\n\n")),
	}
}

// completedStreamResponse is an ordinary streamed answer.
func completedStreamResponse(text string) *http.Response {
	body := map[string]any{"id": "completion-ok", "object": "chat.completion.chunk", "model": "test/model"}
	body["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}}
	chunk, _ := json.Marshal(body)
	body["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}
	last, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + string(chunk) + "\n\ndata: " + string(last) + "\n\ndata: [DONE]\n\n")),
	}
}

// jsonCompletion is a plain, non-streamed answer for the agent's side requests.
func jsonCompletion(text string) *http.Response {
	response := map[string]any{
		"id": "completion-plain", "object": "chat.completion", "model": "test/model",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
	data, _ := json.Marshal(response)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

// runningAgent is a started agent web server and the ways this test talks to it.
type runningAgent struct {
	base   string
	client *http.Client
	stop   func()
}

// startAgent boots the agent binary against its own database in work.
func startAgent(t *testing.T, binary, work string) *runningAgent {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	cmd := exec.CommandContext(t.Context(), binary,
		"-db", filepath.Join(work, "picoclaw.db"), "serve",
		"-socket", "none", "-port", strconv.Itoa(port), "-require-header", "X-ExeDev-Userid")
	cmd.Dir = work
	// Isolate the agent from developer API keys and user configuration.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "SHELLEY_SKIP_VERSION_CHECK=true"}
	logfile, err := os.Create(filepath.Join(work, "agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var stopped bool
	agent := &runningAgent{
		base:   fmt.Sprintf("http://127.0.0.1:%d", port),
		client: &http.Client{Timeout: 15 * time.Second},
	}
	agent.stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logfile.Close()
	}
	t.Cleanup(agent.stop)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := agent.client.Get(agent.base + "/version")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return agent
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	agent.stop()
	log, _ := os.ReadFile(logfile.Name())
	t.Fatalf("agent did not start: %s", log)
	return nil
}

func (a *runningAgent) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, a.base+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-ExeDev-Userid", "test-owner")
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// await polls the conversation until want reports that it says what the test is
// waiting for, and returns that body.
func (a *runningAgent) await(t *testing.T, conversationID string, want func(string) bool, description string) string {
	t.Helper()
	var last string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status, data := a.call(t, "GET", "/api/conversation/"+conversationID, nil)
		if status == 200 {
			last = string(data)
			if want(last) {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; conversation: %s", description, last)
	return ""
}

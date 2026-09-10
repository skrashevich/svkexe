package llmproxy

import (
	"bufio"
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

// Run with SVKEXE_TEST_AGENT_BINARY pointing at a native build. This starts the
// actual preserved web server, then exercises PicoClaw -> svkexe gateway -> LLM
// including a real bash tool, streamed response, and persistence across restart.
func TestPicoClawThroughGateway(t *testing.T) {
	binary := os.Getenv("SVKEXE_TEST_AGENT_BINARY")
	if binary == "" {
		t.Skip("set SVKEXE_TEST_AGENT_BINARY to the native agent build")
	}
	work := t.TempDir()
	var toolSeen, requestSeen atomic.Bool
	gateway := New(Config{APIKey: "provider-key", InternalToken: "agent-token", Models: []string{"test/model"}})
	gateway.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			return nil, fmt.Errorf("wrong upstream key")
		}
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		if req.Model != "test/model" {
			return nil, fmt.Errorf("wrong model %s", req.Model)
		}
		requestSeen.Store(true)
		isToolReply := false
		for _, msg := range req.Messages {
			if msg.Role == "tool" && strings.Contains(string(msg.Content), "pico-tool-ok") {
				isToolReply = true
				toolSeen.Store(true)
			}
		}
		msg := map[string]any{"role": "assistant", "content": "pico-complete"}
		finish := "stop"
		// Requests without tools (e.g. slug generation) remain plain completions.
		if len(req.Tools) > 0 && !isToolReply {
			msg["content"] = ""
			msg["tool_calls"] = []any{map[string]any{"id": "call-pico", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"printf pico-tool-ok"}`}}}
			finish = "tool_calls"
		}
		response := map[string]any{"id": "completion-pico", "object": "chat.completion", "model": "test/model", "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
		data, _ := json.Marshal(response)
		ctype := "application/json"
		if req.Stream {
			if calls, ok := msg["tool_calls"].([]any); ok {
				calls[0].(map[string]any)["index"] = 0
			}
			response["object"] = "chat.completion.chunk"
			response["choices"] = []any{map[string]any{"index": 0, "delta": msg, "finish_reason": nil}}
			chunk, _ := json.Marshal(response)
			response["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
			last, _ := json.Marshal(response)
			data = []byte("data: " + string(chunk) + "\n\ndata: " + string(last) + "\n\ndata: [DONE]\n\n")
			ctype = "text/event-stream"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{ctype}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("POST /api/llm/v1/chat/completions", gateway)
	mux.HandleFunc("GET /api/llm/v1/models", gateway.ServeModels)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 15 * time.Second}
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var payload io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			payload = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, payload)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-ExeDev-Userid", "test-owner")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	start := func() func() {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, "-db", filepath.Join(work, "shelley.db"), "serve", "-socket", "none", "-port", strconv.Itoa(port), "-require-header", "X-ExeDev-Userid")
		cmd.Dir = work
		// Isolate the agent from developer API keys and user configuration.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "SHELLEY_SKIP_VERSION_CHECK=true"}
		logfile, err := os.Create(filepath.Join(work, fmt.Sprintf("agent-%d.log", time.Now().UnixNano())))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = logfile
		cmd.Stderr = logfile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			logfile.Close()
		}
		t.Cleanup(stop)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-deadline.C:
				stop()
				data, _ := os.ReadFile(logfile.Name())
				t.Fatalf("agent did not start: %s", data)
			case <-ticker.C:
				resp, err := client.Get(base + "/version")
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode == 200 {
						return stop
					}
				}
			}
		}
	}
	stop := start()
	status, data := call("GET", "/version", nil)
	if status != 200 || !strings.Contains(string(data), "picoclaw-engine") {
		t.Fatalf("wrong engine: %s", data)
	}
	status, data = call("GET", "/", nil)
	if status != 200 || !strings.Contains(string(data), "<html") {
		t.Fatalf("web UI unavailable: %d", status)
	}
	// The trust header remains required for APIs.
	resp, err := client.Get(base + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 && resp.StatusCode != 401 {
		t.Fatalf("API accepted missing identity: %d", resp.StatusCode)
	}
	status, data = call("POST", "/api/custom-models", map[string]any{"display_name": "svkexe integration", "provider_type": "openai", "endpoint": proxy.URL + "/api/llm/v1", "api_key": "agent-token", "model_name": "test/model", "max_tokens": 8192})
	if status != 200 && status != 201 {
		t.Fatalf("create model: %d %s", status, data)
	}
	var model struct {
		ID string `json:"model_id"`
	}
	json.Unmarshal(data, &model)
	if model.ID == "" {
		t.Fatalf("missing model ID: %s", data)
	}
	status, data = call("POST", "/api/conversations/new", map[string]string{"message": "Run printf pico-tool-ok, then reply pico-complete.", "model": model.ID, "cwd": work})
	if status != 200 && status != 201 {
		t.Fatalf("create conversation: %d %s", status, data)
	}
	var conv struct {
		ID string `json:"conversation_id"`
	}
	json.Unmarshal(data, &conv)
	if conv.ID == "" {
		t.Fatalf("missing conversation ID: %s", data)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", base+"/api/conversation/"+conv.ID+"/stream", nil)
	req.Header.Set("X-ExeDev-Userid", "test-owner")
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stream.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	complete := false
	for scanner.Scan() {
		if toolSeen.Load() && strings.Contains(scanner.Text(), "pico-complete") && !strings.Contains(scanner.Text(), "Run printf") {
			complete = true
			break
		}
	}
	stream.Body.Close()
	if !complete || !toolSeen.Load() || !requestSeen.Load() {
		t.Fatalf("incomplete round trip: complete=%v tool=%v upstream=%v", complete, toolSeen.Load(), requestSeen.Load())
	}
	stop()
	stop = start()
	defer stop()
	status, data = call("GET", "/api/conversation/"+conv.ID, nil)
	if status != 200 || !strings.Contains(string(data), "pico-complete") || !strings.Contains(string(data), "pico-tool-ok") {
		t.Fatalf("history lost across restart: %d %s", status, data)
	}
}

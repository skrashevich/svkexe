package picoclaw

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/secrets"
)

// TestOpenRouterLive is opt-in: it makes real requests using openrouter/free.
// Set SVKEXE_TEST_AGENT_BINARY and SVKEXE_TEST_OPENROUTER_KEY to run it.
func TestOpenRouterLive(t *testing.T) {
	binary := os.Getenv("SVKEXE_TEST_AGENT_BINARY")
	if binary == "" {
		t.Skip("set SVKEXE_TEST_AGENT_BINARY to the native agent build")
	}
	key := os.Getenv("SVKEXE_TEST_OPENROUTER_KEY")
	if key == "" {
		t.Skip("set SVKEXE_TEST_OPENROUTER_KEY for live test")
	}
	work := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 120 * time.Second}
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
				t.Fatalf("agent did not start: %s", strings.ReplaceAll(string(data), key, "[REDACTED]"))
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
	// Persist through the same provider normalization, encryption and model seed
	// used by dashboard saves and VM setup, then reload the real agent.
	stop()
	gatewayDB, err := db.Open(filepath.Join(work, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayDB.Close()
	if _, err := gatewayDB.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	enc := []byte("01234567890123456789012345678901")
	if err := gatewayDB.SaveProviderKey("key", "owner", "openrouter", key, "", "openrouter/free", enc); err != nil {
		t.Fatal(err)
	}
	materializer := secrets.NewMaterializer(gatewayDB, enc, filepath.Join(work, "secrets"))
	connections, err := materializer.ProviderModels("owner")
	if err != nil {
		t.Fatal(err)
	}
	agentDB, err := sql.Open("sqlite", filepath.Join(work, "shelley.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = agentDB.Exec(providerModelsSQL(connections))
	agentDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	stop = start()
	modelID := "svkexe_user:openrouter:openrouter/free"
	status, data = call("POST", "/api/conversations/new", map[string]string{"message": "Run printf pico-tool-ok, then reply pico-complete.", "model": modelID, "cwd": work})
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
	complete, toolSeen := false, false
	for scanner.Scan() {
		payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event struct {
			Messages []struct {
				Type    string `json:"type"`
				LLMData string `json:"llm_data"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		for _, message := range event.Messages {
			var reply struct {
				EndOfTurn bool
				Content   []struct {
					Text       string
					ToolUseID  string
					ToolError  bool
					ToolResult []struct{ Text string }
				}
			}
			if json.Unmarshal([]byte(message.LLMData), &reply) != nil {
				continue
			}
			for _, content := range reply.Content {
				if content.ToolUseID != "" && !content.ToolError {
					for _, result := range content.ToolResult {
						if result.Text == "pico-tool-ok" {
							toolSeen = true
						}
					}
				}
				if message.Type == "agent" && reply.EndOfTurn && strings.TrimSpace(content.Text) == "pico-complete" {
					complete = true
				}
			}
		}

		if complete && toolSeen {
			break
		}
	}

	stream.Body.Close()
	if !complete || !toolSeen {
		t.Fatalf("incomplete live stream: answer=%v tool=%v scanner=%v", complete, toolSeen, scanner.Err())
	}
	stop()
	stop = start()
	defer stop()
	status, data = call("GET", "/api/conversation/"+conv.ID, nil)
	if status != 200 || !strings.Contains(string(data), "pico-complete") || !strings.Contains(string(data), "pico-tool-ok") {
		t.Fatalf("tool result or completion missing after restart: status %d", status)
	}
	persisted, err := sql.Open("sqlite", filepath.Join(work, "shelley.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer persisted.Close()
	var toolCount, answerCount int
	if err := persisted.QueryRow(`SELECT count(*) FROM messages WHERE conversation_id = ? AND EXISTS (SELECT 1 FROM json_each(messages.llm_data, '$.Content') c WHERE json_extract(c.value, '$.ToolUseID') != '' AND json_extract(c.value, '$.ToolError') = 0 AND json_extract(c.value, '$.ToolResult[0].Text') = 'pico-tool-ok')`, conv.ID).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := persisted.QueryRow(`SELECT count(*) FROM messages WHERE conversation_id = ? AND type = 'agent' AND llm_data LIKE '%pico-complete%'`, conv.ID).Scan(&answerCount); err != nil {
		t.Fatal(err)
	}
	if toolCount == 0 || answerCount == 0 {
		t.Fatalf("missing actual tool result or agent answer: tool=%d answer=%d", toolCount, answerCount)
	}
	t.Logf("OpenRouter live round trip passed: saved connection, stream, %d tool result(s), %d answer(s), history after restart", toolCount, answerCount)

}

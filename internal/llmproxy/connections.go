package llmproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/secrets"
)

var connectionClient = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

// ServeConnections forwards native protocol requests using only the authenticated owner's connections.
// Stable connection-qualified IDs distinguish identical model names at different providers.
func ServeConnections(w http.ResponseWriter, r *http.Request, models []secrets.ProviderModel, chosen string) {
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(r.URL.Path, "/api/llm/v1")
	if path == "/models" && r.Method == http.MethodGet {
		type entry struct {
			ID       string `json:"id"`
			Object   string `json:"object"`
			OwnedBy  string `json:"owned_by"`
			Protocol string `json:"protocol"`
		}
		entries := make([]entry, 0, len(models))
		for _, m := range models {
			entries = append(entries, entry{m.ID(), "model", m.Provider, m.Protocol})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": entries})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	protocol := ""
	gemName := ""
	gemAction := ""
	switch path {
	case "/chat/completions":
		protocol = "openai"
	case "/responses":
		protocol = "openai-responses"
	case "/messages":
		protocol = "anthropic"
	default:
		if rest, ok := strings.CutPrefix(path, "/models/"); ok {
			gemName, gemAction, _ = strings.Cut(rest, ":")
			// Connection-qualified model IDs contain colons; split at the last action separator.
			if i := strings.LastIndex(rest, ":"); i >= 0 {
				gemName, gemAction = rest[:i], rest[i+1:]
			}
			if gemAction == "generateContent" || gemAction == "streamGenerateContent" {
				protocol = "gemini"
			}
		}
	}
	if protocol == "" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		http.Error(w, "invalid or oversized request", 400)
		return
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		http.Error(w, "invalid JSON object", 400)
		return
	}
	var requested string
	if raw, ok := payload["model"]; ok && json.Unmarshal(raw, &requested) != nil {
		http.Error(w, "model must be a string", 400)
		return
	}
	if protocol == "gemini" {
		requested = gemName
	}
	if requested == "" {
		requested = chosen
	}
	var selected *secrets.ProviderModel
	for _, m := range models {
		if requested == m.ID() {
			selected = &m
			break
		}
	}
	if selected == nil && requested != "" {
		for _, m := range models {
			if m.Model == requested {
				if selected != nil {
					http.Error(w, "ambiguous model; use its connection-qualified ID from /models", 400)
					return
				}
				selected = &m
			}
		}
	}
	if selected == nil && requested == "" {
		for _, m := range models {
			if m.Protocol == protocol {
				selected = &m
				break
			}
		}
	}
	if selected == nil {
		http.Error(w, "model not available in your LLM connections", 404)
		return
	}
	if selected.Protocol != protocol {
		http.Error(w, "model uses protocol "+selected.Protocol+"; use its native endpoint", 400)
		return
	}
	upstreamURL := strings.TrimRight(selected.BaseURL, "/")
	switch protocol {
	case "anthropic": // The configured base URL is already the full messages endpoint.
	case "gemini":
		upstreamURL += "/models/" + url.PathEscape(selected.Model) + ":" + gemAction
	default:
		upstreamURL += path
	}
	if protocol == "gemini" {
		delete(payload, "model")
	} else {
		payload["model"], _ = json.Marshal(selected.Model)
	}
	body, err = json.Marshal(payload)
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid connection endpoint", 502)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	switch protocol {
	case "anthropic":
		upstream.Header.Set("X-Api-Key", selected.Key)
		version := r.Header.Get("Anthropic-Version")
		if version == "" {
			version = "2023-06-01"
		}
		upstream.Header.Set("Anthropic-Version", version)
		if beta := r.Header.Get("Anthropic-Beta"); beta != "" {
			upstream.Header.Set("Anthropic-Beta", beta)
		}
	case "gemini":
		upstream.Header.Set("X-Goog-Api-Key", selected.Key)
		if gemAction == "streamGenerateContent" {
			q := upstream.URL.Query()
			q.Set("alt", "sse")
			upstream.URL.RawQuery = q.Encode()
		}
	default:
		upstream.Header.Set("Authorization", "Bearer "+selected.Key)
	}
	response, err := connectionClient.Do(upstream)
	if err != nil {
		http.Error(w, "LLM provider request failed", 502)
		return
	}
	defer response.Body.Close()
	// Never echo upstream error bodies: provider errors can contain credential values.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		http.Error(w, "LLM provider returned HTTP "+strconv.Itoa(response.StatusCode), 502)
		return
	}
	for _, header := range []string{"Content-Type", "Retry-After"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	var stream bool
	_ = json.Unmarshal(payload["stream"], &stream)
	if stream || gemAction == "streamGenerateContent" {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(flushingWriter{w}, response.Body)
}

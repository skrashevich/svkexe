package llmproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const openRouterURL = "https://openrouter.ai/api/v1/chat/completions"

// Config holds configuration for the LLM reverse proxy.
type Config struct {
	// APIKey is the OpenRouter API key.
	APIKey string
	// Models is the ordered list of models to try. The proxy tries each
	// model in order until one succeeds.
	Models []string
	// InternalToken is the Bearer token that Shelley must present.
	// If empty, no auth check is performed.
	InternalToken string
}

// Proxy is an OpenAI-compatible reverse proxy that forwards requests
// to OpenRouter, trying models from Config.Models in order.
type Proxy struct {
	cfg    Config
	client *http.Client
}

// New creates a new LLM proxy.
func New(cfg Config) *Proxy {
	return &Proxy{
		cfg: cfg,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// chatRequest is the minimal structure we need to read/modify in the
// OpenAI chat completion request body.
type chatRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages"`
	// Preserve all other fields.
	Extra map[string]json.RawMessage `json:"-"`
}

func (c *chatRequest) UnmarshalJSON(data []byte) error {
	// First unmarshal known fields.
	type plain chatRequest
	if err := json.Unmarshal(data, (*plain)(c)); err != nil {
		return err
	}
	// Then capture everything into Extra.
	if err := json.Unmarshal(data, &c.Extra); err != nil {
		return err
	}
	delete(c.Extra, "model")
	delete(c.Extra, "stream")
	delete(c.Extra, "messages")
	return nil
}

func (c *chatRequest) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(c.Extra)+3)
	for k, v := range c.Extra {
		m[k] = v
	}
	m["model"] = c.Model
	m["stream"] = c.Stream
	m["messages"] = c.Messages
	return json.Marshal(m)
}

// ServeHTTP handles POST requests as OpenAI-compatible chat completions.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth check.
	if p.cfg.InternalToken != "" {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != p.cfg.InternalToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB limit
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	models := p.cfg.Models
	// If the client specified a model and it's in our list, try it first.
	if req.Model != "" {
		models = prioritize(req.Model, models)
	}

	var lastErr error
	for _, model := range models {
		req.Model = model
		reqBody, err := json.Marshal(&req)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, openRouterURL, bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

		resp, err := p.client.Do(upstream)
		if err != nil {
			lastErr = fmt.Errorf("model %s: %w", model, err)
			log.Printf("llmproxy: model %s request failed: %v", model, err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			// Success — forward the response.
			for k, vs := range resp.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(http.StatusOK)
			io.Copy(w, resp.Body)
			resp.Body.Close()
			return
		}

		// Non-200 — read error, log, try next model.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		lastErr = fmt.Errorf("model %s: status %d: %s", model, resp.StatusCode, string(errBody))
		log.Printf("llmproxy: %v", lastErr)
	}

	// All models failed.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("all models failed: %v", lastErr),
			"type":    "proxy_error",
		},
	})
}

// prioritize returns models with preferred first, followed by the rest
// (excluding preferred to avoid duplicates).
func prioritize(preferred string, models []string) []string {
	result := []string{preferred}
	for _, m := range models {
		if m != preferred {
			result = append(result, m)
		}
	}
	return result
}

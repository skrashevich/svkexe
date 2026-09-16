package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// slugifyModelID builds a human-readable model id from the model's upstream
// name and endpoint. The model name comes first since it's the most
// identifying part; the source (host and non-default port) follows. The shape
// is:
//
//	modelname-host(-port)
//
// where the port segment is omitted when it is the provider's default for the
// endpoint's scheme (443 for https, 80 for http) or absent. Every segment is
// lowercased and any run of non-alphanumeric characters collapses to a single
// hyphen. The result never carries a leading/trailing hyphen.
//
// Examples:
//
//	gpt-4o, https://api.openai.com/v1          -> gpt-4o-api-openai-com
//	llama3.1, http://localhost:11434           -> llama3-1-localhost-11434
//	accounts/fireworks/models/glm-5p2,
//	    https://api.fireworks.ai/inference/v1  -> glm-5p2-api-fireworks-ai
func slugifyModelID(endpoint, modelName string) string {
	host, port := hostPort(endpoint)

	// Prefer the last path segment of the model name; upstream names are
	// frequently namespaced (e.g. accounts/.../models/<name>) and only the
	// trailing component is meaningful to a human.
	name := modelName
	if i := strings.LastIndex(name, "/"); i >= 0 && i+1 < len(name) {
		name = name[i+1:]
	}

	parts := make([]string, 0, 3)
	if s := slugify(name); s != "" {
		parts = append(parts, s)
	}
	if s := slugify(host); s != "" {
		parts = append(parts, s)
	}
	if port != "" {
		parts = append(parts, port)
	}

	base := strings.Join(parts, "-")
	if base == "" {
		base = "custom"
	}
	return base
}

// hostPort extracts the host and non-default port from an endpoint. When the
// endpoint can't be parsed as a URL, the whole string is treated as the host.
func hostPort(endpoint string) (host, port string) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint, ""
	}
	host = u.Hostname()
	port = u.Port()
	switch {
	case u.Scheme == "https" && port == "443":
		port = ""
	case u.Scheme == "http" && port == "80":
		port = ""
	}
	return host, port
}

// slugify lowercases s and collapses runs of non-alphanumeric characters into
// single hyphens, trimming any leading/trailing hyphens.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := true // suppress leading hyphen
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// modelIDExists reports whether a model with the given id is already taken,
// either by a stored custom model (DB) or by a known built-in/catalog model
// loaded in the manager. Checking the manager avoids generating a slug that
// collides with a built-in id; such a collision would otherwise cause the
// custom model to be silently skipped at load time.
func (s *Server) modelIDExists(ctx context.Context, modelID string) (bool, error) {
	if s.llmManager != nil && s.llmManager.HasModel(modelID) {
		return true, nil
	}
	_, err := s.db.GetModel(ctx, modelID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

// generateUniqueModelID returns a human-readable model id derived from the
// endpoint and model name, appending a numeric suffix (-2, -3, ...) when needed
// to avoid colliding with an existing model.
func (s *Server) generateUniqueModelID(ctx context.Context, endpoint, modelName string) (string, error) {
	base := slugifyModelID(endpoint, modelName)
	candidate := base
	for i := 2; ; i++ {
		exists, err := s.modelIDExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

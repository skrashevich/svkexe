package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm/predictable"
)

// newCacheKeyTestServer makes a Server wired with the requireHeader so that
// userID extraction has something to look at.
func newCacheKeyTestServer(t *testing.T, requireHeader string) *Server {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	svr := NewServer(database, &testLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		true, "predictable", requireHeader)
	return svr
}

func doCacheKey(t *testing.T, svr *Server, header, userID string, cookies ...*http.Cookie) (*http.Response, cacheKeyResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/cache-key", nil)
	// httptest requests aren't TLS; pretend we're behind an HTTPS proxy
	// so the cookie gets the Secure attribute.
	req.Header.Set("X-Forwarded-Proto", "https")
	if header != "" && userID != "" {
		req.Header.Set(header, userID)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	svr.handleCacheKey(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cache-key: status %d body=%s", resp.StatusCode, w.Body.String())
	}
	var body cacheKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, body
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestCacheKey_IssuesCookieAndStableKey(t *testing.T) {
	t.Parallel()
	svr := newCacheKeyTestServer(t, "X-User")

	resp1, body1 := doCacheKey(t, svr, "X-User", "alice")
	c := findCookie(resp1, cacheCookieName)
	if c == nil || c.Value == "" {
		t.Fatalf("expected cookie set; got %v", resp1.Cookies())
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags wrong: %+v", c)
	}
	if body1.Alg != cacheKeyAlg {
		t.Errorf("alg = %q, want %q", body1.Alg, cacheKeyAlg)
	}
	raw, err := base64.StdEncoding.DecodeString(body1.Key)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key not 32 bytes: %v err=%v", len(raw), err)
	}
	if body1.KeyID == "" {
		t.Errorf("empty key_id")
	}

	// Second call with same cookie returns the same key.
	resp2, body2 := doCacheKey(t, svr, "X-User", "alice", c)
	if findCookie(resp2, cacheCookieName) != nil {
		t.Errorf("second call should not re-issue cookie")
	}
	if body2.Key != body1.Key || body2.KeyID != body1.KeyID {
		t.Errorf("keys diverged on second call")
	}
}

func TestCacheKey_NoSecureFlagOnPlainHTTP(t *testing.T) {
	t.Parallel()
	svr := newCacheKeyTestServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/cache-key", nil)
	// No X-Forwarded-Proto, no TLS — represents a plain-HTTP deploy.
	w := httptest.NewRecorder()
	svr.handleCacheKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	c := findCookie(w.Result(), cacheCookieName)
	if c == nil {
		t.Fatalf("no cookie")
	}
	if c.Secure {
		t.Errorf("cookie should not have Secure on plain HTTP, got %+v", c)
	}
	if !c.HttpOnly {
		t.Errorf("cookie should still be HttpOnly")
	}
}

func TestCacheKey_UserMismatchRotates(t *testing.T) {
	t.Parallel()
	svr := newCacheKeyTestServer(t, "X-User")

	resp1, body1 := doCacheKey(t, svr, "X-User", "alice")
	c := findCookie(resp1, cacheCookieName)

	resp2, body2 := doCacheKey(t, svr, "X-User", "bob", c)
	newC := findCookie(resp2, cacheCookieName)
	if newC == nil {
		t.Fatalf("expected new cookie after user switch")
	}
	if newC.Value == c.Value {
		t.Errorf("cookie should have rotated; got same value")
	}
	if body2.Key == body1.Key || body2.KeyID == body1.KeyID {
		t.Errorf("key should differ for new user")
	}
}

func TestCacheKey_ClearRotates(t *testing.T) {
	t.Parallel()
	svr := newCacheKeyTestServer(t, "X-User")

	resp1, body1 := doCacheKey(t, svr, "X-User", "alice")
	c := findCookie(resp1, cacheCookieName)

	// Clear
	req := httptest.NewRequest(http.MethodPost, "/api/cache-session/clear", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	svr.handleCacheSessionClear(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear: status %d body=%s", w.Code, w.Body.String())
	}
	clearedCookie := findCookie(w.Result(), cacheCookieName)
	if clearedCookie == nil || clearedCookie.MaxAge >= 0 {
		t.Errorf("expected expired cookie in response, got %+v", clearedCookie)
	}

	// After clear, the row is gone. Re-sending the old cookie should
	// still work (server records it again) but produce the SAME key,
	// since master+token are unchanged. To get a *new* key, the client
	// must drop the cookie (which is what the browser will do because
	// the server expired it). Simulate that.
	resp3, body3 := doCacheKey(t, svr, "X-User", "alice")
	newC := findCookie(resp3, cacheCookieName)
	if newC == nil || newC.Value == c.Value {
		t.Fatalf("expected fresh cookie after clear; got %+v", newC)
	}
	if body3.KeyID == body1.KeyID {
		t.Errorf("key_id should differ after rotation")
	}
}

func TestCacheKey_MasterSecretPersists(t *testing.T) {
	t.Parallel()
	svr := newCacheKeyTestServer(t, "")

	ctx := context.Background()
	s1, err := svr.cacheMasterSecret(ctx)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Drop the per-server in-memory cache and re-read; the on-disk row
	// must round-trip to the same bytes.
	svr.cacheMasterSecretMu.Lock()
	svr.cacheMasterSecretCache = nil
	svr.cacheMasterSecretMu.Unlock()
	s2, err := svr.cacheMasterSecret(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(s1) != 32 || len(s2) != 32 {
		t.Fatalf("bad lengths: %d %d", len(s1), len(s2))
	}
	if string(s1) != string(s2) {
		t.Errorf("secret should persist across in-memory cache resets")
	}
}

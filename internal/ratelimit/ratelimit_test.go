package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllow_UnderLimit(t *testing.T) {
	l := New(10, 5)
	for i := 0; i < 5; i++ {
		if !l.Allow("user1") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
}

func TestAllow_ExceedsBurst(t *testing.T) {
	l := New(1, 2)
	// Consume burst
	l.Allow("user1")
	l.Allow("user1")
	// Next request should be denied
	if l.Allow("user1") {
		t.Fatal("request exceeding burst should be denied")
	}
}

func TestAllow_PerUserIsolation(t *testing.T) {
	l := New(1, 1)
	// Exhaust user1's bucket
	l.Allow("user1")
	if l.Allow("user1") {
		t.Fatal("user1 should be rate limited")
	}
	// user2 should still be allowed
	if !l.Allow("user2") {
		t.Fatal("user2 should not be affected by user1's rate limit")
	}
}

func TestAllow_Refill(t *testing.T) {
	l := New(100, 1)
	l.Allow("user1") // consume token
	// Wait for refill at 100 rps (~10ms per token)
	time.Sleep(20 * time.Millisecond)
	if !l.Allow("user1") {
		t.Fatal("token should have been refilled by now")
	}
}

func TestMiddleware_Allow(t *testing.T) {
	l := New(10, 10)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-ExeDev-Userid", "user1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestMiddleware_Deny(t *testing.T) {
	l := New(1, 1)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	makeReq := func() int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-ExeDev-Userid", "user1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	makeReq() // consume burst
	if code := makeReq(); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", code)
	}
}

func TestMiddleware_RetryAfterHeader(t *testing.T) {
	l := New(1, 1)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-ExeDev-Userid", "user1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	makeReq()
	rr := makeReq()
	if rr.Code == http.StatusTooManyRequests && rr.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header should be set on 429 response")
	}
}

func TestMiddleware_FallbackToRemoteAddr(t *testing.T) {
	l := New(10, 10)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No X-ExeDev-Userid header — should fall back to RemoteAddr
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

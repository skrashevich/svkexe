package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"shelley.exe.dev/db"
)

// TestStreamCompressionGzip verifies the SSE stream is gzip-encoded when the
// client advertises gzip in Accept-Encoding, and that messages are flushed
// promptly (no end-of-stream needed for the client to decode).
func TestStreamCompressionGzip(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/api/conversation/"+conv.ConversationID+"/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	// http.DefaultClient would silently decompress; use a transport with
	// DisableCompression so we can verify the Content-Encoding header.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding: got %q want gzip", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary should include Accept-Encoding, got %q", got)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	// gzip.Reader's multistream mode lets it transparently consume
	// concatenated gzip frames; we have a single stream with Flush calls.
	gr.Multistream(false)

	line, err := readSSELine(t, gr, 2*time.Second)
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected SSE data line, got %q", line)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}

// TestStreamCompressionZstd verifies zstd is preferred when both are offered
// and that decoding works without waiting for stream close.
func TestStreamCompressionZstd(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/api/conversation/"+conv.ConversationID+"/stream", nil)
	req.Header.Set("Accept-Encoding", "zstd, gzip")
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding: got %q want zstd", got)
	}

	// Use a single decoder goroutine; the default async decoder will
	// buffer reads and delay surfacing the first message in tests.
	zr, err := zstd.NewReader(resp.Body, zstd.WithDecoderConcurrency(1))
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer zr.Close()

	line, err := readSSELine(t, zr, 2*time.Second)
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected SSE data line, got %q", line)
	}
}

// TestStreamCompressionIdentity verifies plain (identity) encoding still works
// when the client doesn't advertise gzip or zstd.
func TestStreamCompressionIdentity(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/api/conversation/"+conv.ConversationID+"/stream", nil)
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding: got %q want empty", got)
	}

	line, err := readSSELine(t, resp.Body, 2*time.Second)
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected SSE data line, got %q", line)
	}
}

// readSSELine reads up to the first non-empty SSE data line within the given
// timeout. The scanner runs in a goroutine because Read may block waiting on
// more data from the server.
func readSSELine(t *testing.T, r io.Reader, timeout time.Duration) (string, error) {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for s.Scan() {
			line := s.Text()
			if strings.HasPrefix(line, "data: ") {
				ch <- result{line, nil}
				return
			}
		}
		ch <- result{"", s.Err()}
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-time.After(timeout):
		return "", context.DeadlineExceeded
	}
}

// TestStreamCompressionUnifiedEndpoint exercises the same handler on
// /api/stream2 (instead of the legacy /api/conversation/<id>/stream path).
// Both endpoints share runStream, but /api/stream2 is the one that writes a
// list-patch / heartbeat frame before per-conversation work, so we want
// independent coverage that compression headers and framing are correct.
func TestStreamCompressionUnifiedEndpoint(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	conv, err := database.CreateConversation(context.Background(), nil, false, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	t.Run("zstd", func(t *testing.T) {
		req, _ := http.NewRequest("GET", hs.URL+"/api/stream2?conversation="+conv.ConversationID, nil)
		req.Header.Set("Accept-Encoding", "zstd, gzip")
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Content-Encoding"); got != "zstd" {
			t.Fatalf("Content-Encoding: got %q want zstd", got)
		}
		zr, err := zstd.NewReader(resp.Body, zstd.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatalf("zstd reader: %v", err)
		}
		defer zr.Close()
		line, err := readSSELine(t, zr, 2*time.Second)
		if err != nil {
			t.Fatalf("read first SSE line: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("want SSE data line, got %q", line)
		}
	})

	t.Run("gzip", func(t *testing.T) {
		req, _ := http.NewRequest("GET", hs.URL+"/api/stream2?conversation="+conv.ConversationID, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding: got %q want gzip", got)
		}
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		gr.Multistream(false)
		line, err := readSSELine(t, gr, 2*time.Second)
		if err != nil {
			t.Fatalf("read first SSE line: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("want SSE data line, got %q", line)
		}
	})
}

// TestNegotiateContentEncoding covers q-value negotiation edge cases that
// distinguish the parser from a naive strings.Contains check.
func TestNegotiateContentEncoding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header     string
		want       string
		wantAccept bool
	}{
		{"zstd, gzip", "zstd", true},
		{"gzip", "gzip", true},
		{"zstd;q=0, gzip", "gzip", true},
		{"zstd;q=0.0, gzip;q=0.5", "gzip", true},
		{"gzip;q=0", "", true}, // identity still acceptable
		{"br, identity", "", true},
		{"", "", true},
		{"GZIP", "gzip", true}, // case-insensitive coding name
		{"zstd ; q = 1.0", "zstd", true},
		// 406-worthy: no compressed encoding, identity explicitly refused.
		{"identity;q=0", "", false},
		{"gzip;q=0, zstd;q=0, identity;q=0", "", false},
		{"br, identity;q=0", "", false},
		// `*;q=0` rejects anything not explicitly listed; identity isn't listed.
		{"*;q=0", "", false},
		// `*;q=0` but identity explicitly allowed -> identity wins.
		{"*;q=0, identity", "", true},
		// `*;q=0` but gzip explicitly allowed -> gzip wins.
		{"*;q=0, gzip", "gzip", true},
		// `*;q=1` lets us pick a compressed encoding even when identity is refused.
		{"identity;q=0, *;q=1", "zstd", true},
		{"zstd;q=0, identity;q=0, *;q=1", "gzip", true},
		{"zstd;q=0, gzip;q=0, identity;q=0, *;q=1", "", false},
	}
	for _, tc := range cases {
		r := &http.Request{Header: http.Header{"Accept-Encoding": []string{tc.header}}}
		got, acceptable := negotiateContentEncoding(r)
		if got != tc.want || acceptable != tc.wantAccept {
			t.Errorf("negotiateContentEncoding(%q) = (%q,%v), want (%q,%v)",
				tc.header, got, acceptable, tc.want, tc.wantAccept)
		}
	}
}

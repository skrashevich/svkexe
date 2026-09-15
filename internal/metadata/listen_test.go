package metadata

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// The control hook that sets IP_FREEBIND runs on every bind, so a mistake in it
// would stop the listener opening at all — on Linux, where it matters, and on a
// developer's machine, where the no-op sibling has to be equally harmless.
func TestListenerOpensAndServes(t *testing.T) {
	srv := testService(t, sampleIdentity())

	server, err := srv.Start(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	if server.ReadHeaderTimeout == 0 || server.WriteTimeout == 0 {
		t.Error("the metadata listener must not be holdable by a stuck client")
	}
}

// A bind that cannot succeed is an error the caller logs, not a process that
// dies: the gateway serves the dashboard, the API and every VM's traffic
// whether or not the link-local address could be claimed.
func TestStartReportsABindFailureInsteadOfExiting(t *testing.T) {
	srv := testService(t, sampleIdentity())

	// A port outside the range can never be bound on any platform.
	if _, err := srv.Start(t.Context(), "127.0.0.1:99999"); err == nil {
		t.Fatal("binding an impossible address should have failed")
	}
}

// End to end over a real socket, so the handler is exercised through net/http
// rather than only through httptest's recorder.
func TestServesOverARealSocket(t *testing.T) {
	// The loopback address the request arrives from has to be the one the
	// resolver knows, or the service would rightly refuse its own test.
	srv := New(&fakeResolver{byAddr: map[string]*Identity{"127.0.0.1": sampleIdentity()}}, Config{Domain: "svk.exe"})

	listener, err := Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: srv, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	resp, err := http.Get("http://" + listener.Addr().String() + "/latest/meta-data/instance-id")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "c-1" {
		t.Errorf("status %d, body %q", resp.StatusCode, body)
	}
}

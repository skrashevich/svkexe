package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// A request that never finishes on its own must not survive the shutdown.
// This is what an update looks like from the outside: the unit is restarted
// while a VM's agent is mid-turn on the LLM proxy, and until the old process
// exits the new one cannot take the port — so every second it spends holding a
// stream open is a second of 502s from the edge.
func TestStopServerClosesAStreamThatNeverEnds(t *testing.T) {
	streaming := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(streaming)
		<-release
	})}
	go srv.Serve(listener)

	type read struct {
		n   int
		err error
	}
	reads := make(chan read, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + listener.Addr().String() + "/")
		if err != nil {
			reads <- read{err: err}
			return
		}
		defer resp.Body.Close()
		n, err := resp.Body.Read(make([]byte, 1))
		reads <- read{n: n, err: err}
	}()
	select {
	case <-streaming:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never started streaming")
	}

	stopServer(srv, 200*time.Millisecond)

	// The stream is still blocked in the handler, so the only thing that can end
	// it is the shutdown itself. Without that, the process would sit here
	// holding the port for as long as the model kept talking.
	select {
	case got := <-reads:
		if got.err == nil {
			t.Fatalf("the stream survived the shutdown: read %d bytes with no error", got.n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the shutdown; the gateway would still be holding the port")
	}
}

// An idle server stops at once rather than sitting out its grace period.
func TestStopServerReturnsImmediatelyWhenIdle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	go srv.Serve(listener)

	start := time.Now()
	stopServer(srv, 5*time.Second)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("stopping an idle server took %s", took)
	}
}

// The grace period is an outage budget, not a politeness setting: the port is
// already closed while it runs down, so its length is how long the edge has
// nothing to talk to. It was 30s, which made every update a visible outage.
func TestShutdownGraceStaysAnOutageBudget(t *testing.T) {
	if shutdownGrace > 10*time.Second {
		t.Fatalf("shutdownGrace is %s; every update is an outage that long", shutdownGrace)
	}
}

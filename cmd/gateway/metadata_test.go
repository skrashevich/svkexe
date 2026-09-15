package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skrashevich/svkexe/internal/metadata"
)

// The metadata tree is served on its own listener and is attributed by source
// address alone. Reaching it through the public port would mean anyone able to
// set a Host header could read a VM's metadata, so the top handler must know
// nothing about it.
func TestPublicHandlerDoesNotServeMetadata(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handled-By", "api")
		w.WriteHeader(http.StatusOK)
	})
	cp := &stubProxy{aliases: map[string]bool{}}
	h := buildTopHandler("example.com", api, cp)

	for _, tc := range []struct{ host, path string }{
		{host: "example.com", path: "/latest/meta-data/"},
		{host: "example.com", path: "/latest/meta-data/instance-id"},
		{host: metadata.Address, path: "/latest/meta-data/"},
		{host: metadata.DefaultAddr, path: "/latest/api/token"},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("X-Handled-By"); got != "api" {
			t.Errorf("%s%s was handled by %q; the public listener must hand it to the API server, which has no such route",
				tc.host, tc.path, got)
		}
	}
}

// startMetadata is the gateway's whole relationship with the service: it either
// returns a server to shut down later or explains itself and returns nil. It
// must never be the reason the process stops.
func TestStartMetadataIsOptional(t *testing.T) {
	if srv := startMetadata("off", nil, nil, "example.com", ""); srv != nil {
		t.Error("METADATA_ADDR=off should leave the service unstarted")
	}
	if srv := startMetadata("", nil, nil, "example.com", ""); srv != nil {
		t.Error("an empty METADATA_ADDR should leave the service unstarted")
	}
	if srv := startMetadata("127.0.0.1:99999", nil, nil, "example.com", ""); srv != nil {
		t.Error("an address that cannot be bound should leave the service unstarted")
	}

	srv := startMetadata("127.0.0.1:0", nil, nil, "example.com", "203.0.113.9")
	if srv == nil {
		t.Fatal("a bindable address should have produced a running service")
	}
	t.Cleanup(func() { _ = srv.Close() })
}

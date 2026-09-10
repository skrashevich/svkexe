package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubProxy stands in for the container proxy: it records that it was reached
// and answers KnowsHost from a fixed set of custom domains.
type stubProxy struct {
	aliases map[string]bool
	// asked records the hosts KnowsHost was consulted about, so a test can show
	// the gateway's own domain never reaches the alias lookup.
	asked []string
}

func (s *stubProxy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Handled-By", "proxy")
	w.WriteHeader(http.StatusOK)
}

func (s *stubProxy) KnowsHost(host string) bool {
	s.asked = append(s.asked, host)
	return s.aliases[host]
}

func handledBy(t *testing.T, h http.Handler, host string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Header().Get("X-Handled-By")
}

func TestTopHandlerRoutesByHost(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handled-By", "api")
		w.WriteHeader(http.StatusOK)
	})
	cp := &stubProxy{aliases: map[string]bool{"app.example.org": true, "app.example.org:8443": true}}
	h := buildTopHandler("example.com", api, cp)

	cases := []struct {
		name string
		host string
		want string
	}{
		{name: "the gateway itself is the dashboard", host: "example.com", want: "api"},
		{name: "the gateway itself with a port", host: "example.com:8080", want: "api"},
		{name: "a VM subdomain", host: "box.example.com", want: "proxy"},
		{name: "a VM subdomain with a port", host: "box.example.com:8080", want: "proxy"},
		{name: "the agent host", host: "agent-box.example.com", want: "proxy"},
		{name: "a claimed custom domain", host: "app.example.org", want: "proxy"},
		{name: "a claimed custom domain with a port", host: "app.example.org:8443", want: "proxy"},
		{name: "an unrelated host", host: "someone-else.example.org", want: "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := handledBy(t, h, tc.host); got != tc.want {
				t.Errorf("%s was handled by %q, want %q", tc.host, got, tc.want)
			}
		})
	}

	// The dashboard must never depend on an alias lookup succeeding.
	for _, asked := range cp.asked {
		if asked == "example.com" || asked == "example.com:8080" {
			t.Errorf("the gateway's own domain reached the alias lookup: %q", asked)
		}
	}
}

// Without a configured domain there is no subdomain namespace, so only an
// explicitly claimed custom domain routes to a VM.
func TestTopHandlerWithoutDomain(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handled-By", "api")
		w.WriteHeader(http.StatusOK)
	})
	cp := &stubProxy{aliases: map[string]bool{"app.example.org": true}}
	h := buildTopHandler("", api, cp)

	if got := handledBy(t, h, "box.example.com"); got != "api" {
		t.Errorf("subdomain without a configured domain: %q, want api", got)
	}
	if got := handledBy(t, h, "app.example.org"); got != "proxy" {
		t.Errorf("claimed custom domain without a configured domain: %q, want proxy", got)
	}
}

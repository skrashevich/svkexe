package dashboard

import "testing"

func TestAllowedWebSocketOrigin(t *testing.T) {
	if !allowedWebSocketOrigin("example.com", "https://example.com") {
		t.Error("expected https base domain")
	}
	if allowedWebSocketOrigin("example.com", "https://evil.com") {
		t.Error("expected foreign origin rejected")
	}
	if !allowedWebSocketOrigin("", "https://anything.test") {
		t.Error("empty domain should allow dev origins")
	}
}

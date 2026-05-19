package dashboard

import "strings"

// allowedWebSocketOrigin reports whether the Origin header is permitted for
// dashboard WebSocket upgrades. When domain is empty (local dev), all origins
// are allowed.
func allowedWebSocketOrigin(domain, origin string) bool {
	if domain == "" {
		return true
	}
	if origin == "" {
		return true
	}
	origin = strings.TrimSuffix(strings.ToLower(origin), "/")
	d := strings.ToLower(domain)
	for _, allowed := range []string{"https://" + d, "http://" + d} {
		if origin == allowed {
			return true
		}
	}
	return false
}

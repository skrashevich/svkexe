package metrics

import (
	"net/http"
	"strings"
)

// BearerToken, when non-empty, protects /metrics with Authorization: Bearer <token>.
var BearerToken string

// ProtectHandler wraps next with bearer-token auth when BearerToken is set.
func ProtectHandler(next http.Handler) http.Handler {
	if BearerToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != BearerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

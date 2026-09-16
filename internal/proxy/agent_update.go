package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
)

// Called only after agent-host authentication and ownership enforcement.
func (p *ContainerProxy) agentUpdate(w http.ResponseWriter, r *http.Request, c *db.Container) {
	w.Header().Set("Cache-Control", "no-store")
	method := http.MethodGet
	if r.URL.Path == "/upgrade" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if method == http.MethodPost {
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || (r.Header.Get("Origin") != "" && origin.Host != r.Host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if c.Status != "running" {
		http.Error(w, "VM is not running", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	// Binary transfer and readiness can exceed the gateway's normal write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
	if method == http.MethodPost {
		if err := picoclaw.Update(ctx, p.runtime, c.IncusName); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	info, err := picoclaw.CheckUpdate(ctx, p.runtime, c.IncusName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

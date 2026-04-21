package proxy

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/svkexe/platform/internal/db"
	"github.com/svkexe/platform/internal/runtime"
)

const shelleyPort = 9000

// ContainerProxy routes subdomain requests to the appropriate container.
type ContainerProxy struct {
	db      *db.DB
	runtime runtime.ContainerRuntime
	domain  string // base domain, e.g. "example.com"
}

// New creates a ContainerProxy.
func New(database *db.DB, rt runtime.ContainerRuntime, domain string) *ContainerProxy {
	return &ContainerProxy{
		db:      database,
		runtime: rt,
		domain:  domain,
	}
}

// ServeHTTP handles an incoming request by resolving the container from the
// subdomain, enforcing ownership, and reverse-proxying to the container's
// Shelley port.
func (p *ContainerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := p.extractSubdomain(r.Host)
	if !ok {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}

	// Look up container by name (ownership check follows).
	container, err := p.db.GetContainerByNameOnly(name)
	if err == sql.ErrNoRows {
		http.Error(w, "container not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Security Invariant S2 / S3: verify caller owns this container.
	userID := r.Header.Get("X-ExeDev-Userid")
	if userID == "" || userID != container.OwnerID {
		// If not owner, check for shared link token.
		token := r.URL.Query().Get("share")
		if token == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		link, err := p.db.GetSharedLinkByToken(token)
		if err != nil || link.ContainerID != container.ID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Valid shared link — proceed.
	}

	// Reject requests to stopped containers.
	if !isRunning(container.Status) {
		http.Error(w, "container is not running", http.StatusServiceUnavailable)
		return
	}

	if container.IPAddress == "" {
		http.Error(w, "container has no IP address", http.StatusServiceUnavailable)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(container.IPAddress, fmt.Sprintf("%d", shelleyPort)),
	}

	proxy := newReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

// extractSubdomain parses "{name}.{domain}" from the Host header.
// Returns the subdomain name and true on success.
func (p *ContainerProxy) extractSubdomain(host string) (string, bool) {
	// Strip port if present.
	h := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// Ensure it's not an IPv6 address without brackets.
		if strings.Count(host, ":") == 1 {
			h = host[:idx]
		}
	}

	suffix := "." + p.domain
	if !strings.HasSuffix(h, suffix) {
		return "", false
	}

	name := strings.TrimSuffix(h, suffix)
	if name == "" || strings.Contains(name, ".") {
		// Empty prefix or nested subdomain — reject.
		return "", false
	}
	return name, true
}

// isRunning returns true for statuses considered "running".
func isRunning(status string) bool {
	return strings.EqualFold(status, "running") || strings.EqualFold(status, "started")
}

// newReverseProxy builds an httputil.ReverseProxy pointed at target with
// sensible timeouts and SSE/WebSocket support.
func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport

	// FlushInterval -1 enables immediate flushing, required for SSE and
	// streaming responses. Go 1.20+ handles WebSocket upgrades natively
	// inside ReverseProxy when FlushInterval is set.
	proxy.FlushInterval = -1

	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
		// Preserve the original Host so the backend can log it.
		pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
	}

	return proxy
}

package proxy

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

const agentPort = 9000

// sessionCookieName mirrors api.SessionCookieName — redeclared here to avoid
// importing the api package (which would create an import cycle).
const sessionCookieName = "svkexe_session"

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
// PicoClaw port.
func (p *ContainerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	info, ok := p.extractSubdomain(r.Host)
	if !ok {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}

	// Keep old Shelley links working while exposing the PicoClaw service name.
	if info.Service != "" && info.Service != "shelley" && info.Service != "picoclaw" {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}

	// Security Invariant S2 / S3: identity comes from the session cookie — we
	// never trust incoming X-ExeDev-* headers for subdomain traffic.
	userID := p.authenticate(r)
	r.Header.Del("X-ExeDev-Userid")
	r.Header.Del("X-ExeDev-Email")

	var container *db.Container
	var err error

	shareToken := r.URL.Query().Get("share")
	if shareToken == "" {
		if cookie, err := r.Cookie("svkexe_share"); err == nil {
			shareToken = cookie.Value
		}
	}
	if shareToken != "" {
		link, linkErr := p.db.GetSharedLinkByToken(shareToken)
		if linkErr != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		container, err = p.db.GetContainerByID(link.ContainerID)
		if err == sql.ErrNoRows {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if container.Name != info.ContainerName {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// The agent requires a trusted identity even for shared access. A
		// host-only cookie carries the grant to subsequent API and asset calls;
		// validating it on every request makes revocation immediate.
		r.Header.Set("X-ExeDev-Userid", "share:"+link.ID)
		if r.URL.Query().Get("share") != "" {
			http.SetCookie(w, &http.Cookie{Name: "svkexe_share", Value: shareToken,
				Path: "/", HttpOnly: true, Secure: p.domain != "", SameSite: http.SameSiteLaxMode,
			})
			query := r.URL.Query()
			query.Del("share")
			r.URL.RawQuery = query.Encode()
		}
	} else if userID != "" {
		container, err = p.db.GetContainerByName(info.ContainerName, userID)
		if err == sql.ErrNoRows {
			http.Error(w, "container not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r.Header.Set("X-ExeDev-Userid", userID)
	} else {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "https://"+p.domain+"/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Reject requests to stopped containers.
	if !isRunning(container.Status) {
		http.Error(w, "container is not running", http.StatusServiceUnavailable)
		return
	}

	if container.IPAddress == "" {
		// Try to fetch IP from runtime and persist it.
		rtc, err := p.runtime.Get(r.Context(), container.IncusName)
		if err != nil {
			log.Printf("proxy: runtime.Get(%s) failed: %v", container.IncusName, err)
			http.Error(w, "container has no IP address", http.StatusServiceUnavailable)
			return
		}
		if rtc.IP == "" {
			log.Printf("proxy: runtime.Get(%s) returned empty IP", container.IncusName)
			http.Error(w, "container has no IP address", http.StatusServiceUnavailable)
			return
		}
		container.IPAddress = rtc.IP
		_ = p.db.UpdateContainerStatus(container.ID, container.Status, rtc.IP)
		log.Printf("proxy: resolved IP %s for %s", rtc.IP, container.IncusName)
	}

	target := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(container.IPAddress, fmt.Sprintf("%d", agentPort)),
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	proxy := newReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

// subdomainInfo holds the result of parsing a subdomain.
type subdomainInfo struct {
	// ContainerName is the VM name portion of the subdomain.
	ContainerName string
	// Service is the optional service prefix (e.g. "shelley"). Empty for direct VM access.
	Service string
}

// extractSubdomain parses "{name}.{domain}" or "{service}.{name}.{domain}"
// from the Host header. Returns the parsed info and true on success.
func (p *ContainerProxy) extractSubdomain(host string) (subdomainInfo, bool) {
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
		return subdomainInfo{}, false
	}

	prefix := strings.TrimSuffix(h, suffix)
	if prefix == "" {
		return subdomainInfo{}, false
	}

	// Check for service.name pattern (e.g. "picoclaw.my-vm").
	if idx := strings.IndexByte(prefix, '.'); idx != -1 {
		service := prefix[:idx]
		name := prefix[idx+1:]
		if name == "" || strings.Contains(name, ".") {
			return subdomainInfo{}, false
		}
		return subdomainInfo{ContainerName: name, Service: service}, true
	}

	return subdomainInfo{ContainerName: prefix}, true
}

// isRunning returns true for statuses considered "running".
func isRunning(status string) bool {
	return strings.EqualFold(status, "running") || strings.EqualFold(status, "started")
}

// authenticate resolves the session cookie to a user ID. Returns the empty
// string when the cookie is absent, the session expired, or lookups fail.
func (p *ContainerProxy) authenticate(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	sess, err := p.db.GetSession(c.Value)
	if err != nil {
		return ""
	}
	return sess.UserID
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

	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
		},
	}

	return proxy
}

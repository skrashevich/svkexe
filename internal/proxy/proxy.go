package proxy

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
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

	// Security Invariant S2 / S3: identity comes from the session cookie — we
	// never trust incoming X-ExeDev-* headers for subdomain traffic.
	userID := p.authenticate(r)
	r.Header.Del("X-ExeDev-Userid")
	r.Header.Del("X-ExeDev-Email")

	var container *db.Container
	var err error

	// A published workload is reachable without any credential. Only the bare
	// VM host qualifies: the agent can run commands, and an explicit-port host
	// would expose listeners the owner never chose to publish.
	if info.Kind == routeApp && info.Port == 0 && userID == "" {
		public, publicErr := p.db.GetContainerByNameOnly(info.ContainerName)
		if publicErr == nil && public.AppPublic {
			p.forward(w, r, public, public.AppPort)
			return
		}
	}

	shareToken := r.URL.Query().Get("share")
	if shareToken == "" {
		if cookie, err := r.Cookie("svkexe_share"); err == nil {
			shareToken = cookie.Value
		}
	}
	// A share grants the workload, never the agent: handing out a shell is not
	// what "share this VM" should mean.
	if shareToken != "" && info.Kind == routeAgent {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
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
		// A host-only cookie carries the grant to subsequent API and asset calls;
		// validating it on every request makes revocation immediate.
		if info.Kind == routeAgent {
			r.Header.Set("X-ExeDev-Userid", "share:"+link.ID)
		}
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
		// Only the agent consumes this header; the workload authenticates its
		// own users and must not treat a gateway header as proof of identity.
		if info.Kind == routeAgent {
			r.Header.Set("X-ExeDev-Userid", userID)
		}
	} else {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "https://"+p.domain+"/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	port := agentPort
	if info.Kind == routeApp {
		port = container.AppPort
		if info.Port != 0 {
			port = info.Port
		}
	}
	p.forward(w, r, container, port)
}

// forward proxies an authorized request to the given in-VM port.
func (p *ContainerProxy) forward(w http.ResponseWriter, r *http.Request, container *db.Container, port int) {
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
		Host:   net.JoinHostPort(container.IPAddress, strconv.Itoa(port)),
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	proxy := newReverseProxy(target)
	// A dead port is the owner's own misconfiguration, not a gateway fault, so
	// say which port had no listener instead of a bare "internal error".
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy: %s port %d unreachable: %v", container.IncusName, port, err)
		http.Error(w, fmt.Sprintf("nothing is listening on port %d in this VM", port), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// routeKind distinguishes the two things a VM exposes.
type routeKind int

const (
	// routeApp is the user's own workload — the point of the VM.
	routeApp routeKind = iota
	// routeAgent is the PicoClaw web interface.
	routeAgent
)

// explicitPortRE splits the "{port}-{name}" workload host.
var explicitPortRE = regexp.MustCompile(`^([0-9]{1,5})-(.+)$`)

// subdomainInfo holds the result of parsing a subdomain.
type subdomainInfo struct {
	// ContainerName is the VM name portion of the subdomain.
	ContainerName string
	// Kind selects the workload or the agent.
	Kind routeKind
	// Port overrides the VM's configured app port. Zero means "use the
	// configured one" and is the only form eligible for public access.
	Port int
}

// extractSubdomain parses the Host header into a route. Recognized forms:
//
//	{name}.{domain}          the workload, on the VM's configured port
//	{port}-{name}.{domain}   the workload, on an explicit port
//	agent-{name}.{domain}    the PicoClaw interface
//	{picoclaw|shelley}.{name}.{domain}  legacy agent links
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

	// Legacy "{service}.{name}" links from before the agent moved to its own
	// single-label host. Kept working; they need extra certificate coverage.
	if service, name, nested := strings.Cut(prefix, "."); nested {
		if service != "picoclaw" && service != "shelley" {
			return subdomainInfo{}, false
		}
		if !db.ValidContainerName(name) {
			return subdomainInfo{}, false
		}
		return subdomainInfo{ContainerName: name, Kind: routeAgent}, true
	}

	if name, ok := strings.CutPrefix(prefix, db.AgentHostPrefix); ok {
		if !db.ValidContainerName(name) {
			return subdomainInfo{}, false
		}
		return subdomainInfo{ContainerName: name, Kind: routeAgent}, true
	}

	if m := explicitPortRE.FindStringSubmatch(prefix); m != nil {
		port, err := strconv.Atoi(m[1])
		if err != nil || !db.ValidAppPort(port) {
			return subdomainInfo{}, false
		}
		if !db.ValidContainerName(m[2]) {
			return subdomainInfo{}, false
		}
		return subdomainInfo{ContainerName: m[2], Kind: routeApp, Port: port}, true
	}

	if !db.ValidContainerName(prefix) {
		return subdomainInfo{}, false
	}
	return subdomainInfo{ContainerName: prefix, Kind: routeApp}, true
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

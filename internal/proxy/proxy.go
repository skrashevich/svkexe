package proxy

import (
	"database/sql"
	"errors"
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

// shareCookieName carries a share grant across the requests a page makes after
// the one that presented the token.
const shareCookieName = "svkexe_share"

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
		// A name inside the gateway's own domain that does not parse is a
		// malformed VM host, not somebody's custom domain — saying "invalid
		// host" is more useful there than a lookup that cannot succeed. With no
		// domain configured there is no such namespace, and every host that
		// reaches here is one KnowsHost already claimed.
		host := aliasHost(r.Host)
		domain := strings.ToLower(p.domain)
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		p.serveAlias(w, r, host)
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

	shareToken := shareTokenFrom(r)
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
		p.acceptShareCookie(w, r, shareToken)
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

	if info.Kind == routeAgent && (r.URL.Path == "/version-check" || r.URL.Path == "/upgrade") {
		p.agentUpdate(w, r, container)
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

// KnowsHost reports whether host is a custom domain this gateway serves. The
// top-level handler asks before routing a request whose Host is not a VM
// subdomain, so an unrelated name still lands on the API server instead of
// being answered by the proxy.
func (p *ContainerProxy) KnowsHost(host string) bool {
	h := aliasHost(host)
	if h == "" {
		return false
	}
	// The gateway's own namespace is routed by subdomain and can never be an
	// alias; ValidAliasHostname refuses to store one, and checking here keeps a
	// stale row from ever shadowing a VM host.
	if domain := strings.ToLower(p.domain); domain != "" && (h == domain || strings.HasSuffix(h, "."+domain)) {
		return false
	}
	_, err := p.db.GetVerifiedAliasByHostname(h)
	return err == nil
}

// serveAlias handles a request that arrived on a custom domain. An alias
// reaches the workload and nothing else: the agent is a shell, and no hostname
// an owner can add is allowed to become one.
func (p *ContainerProxy) serveAlias(w http.ResponseWriter, r *http.Request, host string) {
	// Nothing downstream may read an identity the gateway did not set, and on a
	// custom domain the gateway asserts none at all.
	r.Header.Del("X-ExeDev-Userid")
	r.Header.Del("X-ExeDev-Email")

	alias, err := p.db.GetVerifiedAliasByHostname(host)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	container, err := p.db.GetContainerByID(alias.ContainerID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if container.AppPublic {
		p.forward(w, r, container, container.AppPort)
		return
	}

	// The session cookie is scoped to the gateway's own domain and is never
	// sent to a custom one, so there is no signed-in owner to recognise here. A
	// share link is the only credential that travels with the request.
	if token := shareTokenFrom(r); token != "" {
		link, linkErr := p.db.GetSharedLinkByToken(token)
		if linkErr == nil && link.ContainerID == container.ID {
			p.acceptShareCookie(w, r, token)
			p.forward(w, r, container, container.AppPort)
			return
		}
	}

	http.Error(w, "this VM's workload is not published — publish it from the dashboard or open this domain through a share link", http.StatusForbidden)
}

// shareTokenFrom reads a share token from the query string, falling back to the
// cookie a previous request left behind.
func shareTokenFrom(r *http.Request) string {
	if token := r.URL.Query().Get("share"); token != "" {
		return token
	}
	if cookie, err := r.Cookie(shareCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// acceptShareCookie turns a token that arrived in the query string into a
// host-only cookie, so the grant carries to the API and asset calls the page
// makes next, and drops it from the URL the workload sees. It is re-validated
// on every request, which is what makes revocation immediate.
func (p *ContainerProxy) acceptShareCookie(w http.ResponseWriter, r *http.Request, token string) {
	if r.URL.Query().Get("share") == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: shareCookieName, Value: token,
		Path: "/", HttpOnly: true, Secure: p.domain != "", SameSite: http.SameSiteLaxMode,
	})
	query := r.URL.Query()
	query.Del("share")
	r.URL.RawQuery = query.Encode()
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
	h := hostWithoutPort(host)

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

// hostWithoutPort strips the ":port" a browser appends to the Host header.
// Case is deliberately left alone: VM subdomains are matched exactly, so that
// "UPPER.example.com" stays a name no VM answers to rather than silently
// resolving to a different VM than the one that was typed.
func hostWithoutPort(host string) string {
	h := host
	// A bare IPv6 address has several colons and no port to strip.
	if idx := strings.LastIndex(host, ":"); idx != -1 && strings.Count(host, ":") == 1 {
		h = host[:idx]
	}
	return h
}

// aliasHost normalises a Host header for an alias lookup. Unlike a VM
// subdomain, a custom domain is a name its owner typed into their own DNS, and
// DNS is case-insensitive — matching it strictly would reject the very
// hostname the certificate was issued for.
func aliasHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostWithoutPort(host)), "."))
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

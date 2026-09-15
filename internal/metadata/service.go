package metadata

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	// Address is the link-local address every cloud-aware tool reaches for. It is
	// where VMs reach the service, and deliberately not where the gateway binds:
	// the host would have to take the address as its own to bind it, which on a
	// cloud VPS would cut the host off from its provider's own metadata service
	// at the very same address. The host redirects bridge traffic to DefaultAddr
	// instead — see scripts/install-metadata-units.sh.
	Address = "169.254.169.254"
	// DefaultAddr is where the gateway listens. It is the VM bridge's own
	// address on an unprivileged port, so the listener needs no capability and is
	// not reachable from the host's other networks.
	DefaultAddr = "10.100.0.1:8081"

	// DefaultRegion and the partition name stand in for the fields EC2 fills
	// with its own topology. A caller that branches on them gets a stable
	// answer that cannot be mistaken for a real AWS region.
	DefaultRegion  = "svkexe"
	partition      = "svkexe"
	typePrefix     = "svkexe"
	internalDomain = "svkexe.internal"

	// documentVersion is the schema version EC2 stamps its identity document
	// with. Tooling parses the document by it, so it is reported as-is.
	documentVersion = "2017-09-30"

	// versionIndex is what the root path answers with: the API versions this
	// service implements. Unlike a directory listing inside the tree the entries
	// carry no trailing slash, which is how EC2 renders it.
	versionIndex = "latest\n"

	tokenHeader    = "X-aws-ec2-metadata-token"
	tokenTTLHeader = "X-aws-ec2-metadata-token-ttl-seconds"
	tokenPath      = "/latest/api/token"

	// minTokenTTL and maxTokenTTL are EC2's limits, in seconds.
	minTokenTTL = 1
	maxTokenTTL = 21600
)

// Config is the deployment-wide half of what the service publishes: the facts
// that are the same for every VM.
type Config struct {
	// Domain is the gateway's base domain, used for the public hostname and the
	// service domain. Empty means subdomain routing is not configured and those
	// keys are not published.
	Domain string
	// PublicIPs are the gateway's own public addresses, as configured for the
	// custom-domain DNS check. The first is published as public-ipv4.
	PublicIPs []string
	// ImageID is the image VMs are built from, reported as ami-id. It is a
	// deployment-wide answer because the platform does not record per-VM images.
	ImageID string
	// Region and Zone stand in for EC2's placement. Empty values default to
	// DefaultRegion and "<region>-a".
	Region string
	Zone   string
	// Now is the clock, injectable so token expiry can be tested without
	// sleeping. Nil means time.Now.
	Now func() time.Time
	// Limiter bounds how fast one address may ask. Every answer costs a database
	// read or two, on the pool the dashboard, the API and the SSH gateway share,
	// so an unbounded loop inside one VM is everyone's outage — and nothing here
	// authenticates, so there is no account to hold responsible. Nil means
	// unlimited, which is only for tests.
	Limiter Limiter
}

// Limiter is the slice of internal/ratelimit the service uses.
type Limiter interface {
	Allow(key string) bool
}

// The default rate limit: what one VM may ask per second, and the burst it may spend
// at once. A VM reads its own metadata a handful of times per boot and then
// occasionally; this is generous for that and useless for a loop.
const (
	DefaultRateLimitRPS   = 5
	DefaultRateLimitBurst = 20
)

// Service is the metadata HTTP API. It is an http.Handler so that it can be
// tested with httptest and served on its own listener, which is what keeps it
// off the gateway's public port.
type Service struct {
	resolver Resolver
	cfg      Config
	tokens   *tokenStore
}

// New builds the service over a resolver, applying the defaults Config leaves open.
func New(r Resolver, cfg Config) *Service {
	if cfg.Region == "" {
		cfg.Region = DefaultRegion
	}
	if cfg.Zone == "" {
		cfg.Zone = cfg.Region + "-a"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{resolver: r, cfg: cfg, tokens: newTokenStore(cfg.Now)}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "EC2ws")

	// A request that has been through a proxy is refused outright, header and
	// all. The classic way to steal instance credentials is to talk an
	// in-VM proxy or a server-side fetcher into requesting this address on your
	// behalf, and such a request carries X-Forwarded-For. EC2 refuses it for
	// token requests only; refusing it everywhere costs nothing, because nothing
	// that legitimately reads its own metadata is behind a proxy.
	if _, proxied := r.Header[http.CanonicalHeaderKey("X-Forwarded-For")]; proxied {
		s.fail(w, http.StatusMisdirectedRequest, "the metadata service refuses proxied requests")
		return
	}

	addr, ok := callerAddr(r.RemoteAddr)
	if !ok {
		s.fail(w, http.StatusForbidden, "forbidden")
		return
	}
	// Keyed on the address rather than on the VM, deliberately: the limit has to
	// apply before anything is resolved, or a flood from an unattributable
	// address would still cost a lookup per request.
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(addr.String()) {
		w.Header().Set("Retry-After", "1")
		s.fail(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	id, err := s.resolver.Resolve(r.Context(), addr)
	switch {
	case errors.Is(err, ErrUnknownCaller):
		// Nothing about what does exist: a caller that is not a VM must not be
		// able to tell "no such VM" from "not your VM".
		s.fail(w, http.StatusForbidden, "forbidden")
		return
	case err != nil:
		s.fail(w, http.StatusServiceUnavailable, "metadata is temporarily unavailable")
		return
	case id == nil:
		s.fail(w, http.StatusForbidden, "forbidden")
		return
	}

	if cleanPath(r.URL.Path) == tokenPath {
		s.issueToken(w, r, id)
		return
	}

	// A token is optional — IMDSv1 still answers, so tooling that predates
	// IMDSv2 keeps working — but a token that is presented must be valid and
	// must be this VM's own. One VM handing its token to another must not turn
	// into the other reading its metadata.
	if tok := r.Header.Get(tokenHeader); tok != "" {
		if !s.tokens.valid(tok, id.ContainerID) {
			s.fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.fail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	s.serveTree(w, r, id)
}

// serveTree answers a data request out of the caller's own tree.
func (s *Service) serveTree(w http.ResponseWriter, r *http.Request, id *Identity) {
	requested := cleanPath(r.URL.Path)
	if requested == "/" {
		s.write(w, versionIndex)
		return
	}

	segments := strings.Split(strings.Trim(requested, "/"), "/")
	n := buildTree(id, s.cfg)
	if segments[0] != n.name {
		s.fail(w, http.StatusNotFound, "not found")
		return
	}
	for _, segment := range segments[1:] {
		if n = n.child(segment); n == nil {
			s.fail(w, http.StatusNotFound, "not found")
			return
		}
	}
	if n.isDir() {
		s.write(w, n.listing())
		return
	}
	// A leaf asked for as a directory is not that leaf. Answering anyway would
	// let "…/local-ipv4/" and "…/local-ipv4" be different spellings of one key,
	// and a caller that joined a path with a stray slash would never learn it.
	if strings.HasSuffix(requested, "/") {
		s.fail(w, http.StatusNotFound, "not found")
		return
	}
	s.write(w, n.value)
}

// issueToken implements the IMDSv2 token endpoint.
func (s *Service) issueToken(w http.ResponseWriter, r *http.Request, id *Identity) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		s.fail(w, http.StatusMethodNotAllowed, "the token endpoint accepts PUT only")
		return
	}
	raw := r.Header.Get(tokenTTLHeader)
	seconds, err := strconv.Atoi(raw)
	if raw == "" || err != nil || seconds < minTokenTTL || seconds > maxTokenTTL {
		s.fail(w, http.StatusBadRequest, tokenTTLHeader+" must be between "+
			strconv.Itoa(minTokenTTL)+" and "+strconv.Itoa(maxTokenTTL))
		return
	}
	token, err := s.tokens.issue(id.ContainerID, time.Duration(seconds)*time.Second)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "could not issue a token")
		return
	}
	w.Header().Set(tokenTTLHeader, strconv.Itoa(seconds))
	s.write(w, token)
}

func (s *Service) write(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// fail answers with a status and a fixed reason. The reason never names a VM,
// a path that does exist or an internal error, so that a caller cannot map the
// platform by reading the failures.
func (s *Service) fail(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(reason + "\n"))
}

// callerAddr extracts the address the request arrived from. Only the socket is
// consulted: this is the service's entire notion of who is asking, so nothing a
// caller can set may reach it.
func callerAddr(remote string) (netip.Addr, bool) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	// A scoped address ("fe80::1%eth0") does not parse with its zone attached,
	// and the zone says nothing about identity.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	// A VM reaching the service over IPv4 may appear as ::ffff:10.0.0.2 if the
	// listener is dual-stack; that is the same caller as 10.0.0.2.
	return addr.Unmap(), true
}

// cleanPath normalises a request path to a single leading slash and no duplicate
// separators, preserving a single trailing slash because the tree treats
// "…/placement/" and "…/placement" alike but "…/local-ipv4/" as a mistake.
//
// A ".." segment is deliberately kept as a literal, which makes it match no node
// and answer 404. Nothing here touches a filesystem, so collapsing it would only
// invent a second spelling for paths that already have one.
func cleanPath(p string) string {
	trailing := strings.HasSuffix(p, "/")
	fields := make([]string, 0, 8)
	for _, segment := range strings.Split(p, "/") {
		if segment != "" && segment != "." {
			fields = append(fields, segment)
		}
	}
	out := "/" + strings.Join(fields, "/")
	if trailing && out != "/" {
		out += "/"
	}
	return out
}

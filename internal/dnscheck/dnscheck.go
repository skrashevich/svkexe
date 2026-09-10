// Package dnscheck confirms that a hostname actually resolves to this
// gateway before the hostname is trusted as a VM alias. Without this check,
// a user could claim any domain they don't control: the gateway would
// happily route traffic for it and Caddy would attempt to issue a TLS
// certificate for a hostname that never pointed here, which either fails
// loudly (rate-limited by the CA) or, worse, succeeds because the domain
// briefly pointed elsewhere. Requiring proof-of-pointing up front avoids
// both failure modes.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrPointsElsewhere means the lookup succeeded and the answer was definite:
// this hostname resolves somewhere that is not this gateway.
//
// It exists so a periodic re-check can tell "the owner repointed their domain"
// from "we could not find out". Only the former justifies taking a working
// custom domain out of service; treating a DNS hiccup, or a gateway that has
// momentarily lost track of its own address, as proof of a move would break
// every alias at once for reasons that have nothing to do with their owners.
var ErrPointsElsewhere = errors.New("hostname does not point at this gateway")

// gatewayIPTTL bounds how long a resolved DOMAIN address set is trusted
// before Verify re-resolves it. DNS for the gateway's own domain can change
// (failover, renumbering), but re-resolving on every alias check would turn
// a burst of verifications — e.g. an admin reattaching several aliases at
// once — into a burst of redundant lookups.
const gatewayIPTTL = 5 * time.Minute

// Verifier confirms that a hostname resolves to this gateway, either
// directly (an A/AAAA record) or transitively (a CNAME onto the gateway's
// own DOMAIN). Either setup produces an overlapping IP address set, which is
// the only thing Verify actually checks — it does not care how the owner's
// DNS is structured.
type Verifier struct {
	// domain is the gateway's own DOMAIN, used to discover which addresses
	// this gateway answers on when staticIPs is not set.
	domain string

	// staticIPs, when non-empty, replaces DNS resolution of domain. This is
	// for deployments behind a load balancer or NAT where DOMAIN does not
	// resolve to the gateway's real public address(es) — the operator sets
	// GATEWAY_PUBLIC_IPS instead and we trust it outright, since it never
	// goes stale the way a cached DNS answer can.
	staticIPs []netip.Addr

	// lookupIP resolves a hostname to its addresses. It is a field rather
	// than a direct net.DefaultResolver call so tests can substitute a
	// deterministic, network-free implementation.
	lookupIP func(ctx context.Context, host string) ([]net.IP, error)

	mu        sync.Mutex
	cachedIPs []netip.Addr
	cachedAt  time.Time
}

// New builds a Verifier for the given gateway domain. staticIPs, when
// non-empty, is used verbatim instead of resolving domain — see the
// Verifier.staticIPs field doc for why. Unparseable entries in staticIPs are
// dropped; New does not fail on them because a single malformed value in an
// operator-supplied env var shouldn't take down alias verification for every
// other well-formed one.
func New(domain string, staticIPs []string) *Verifier {
	v := &Verifier{
		domain: strings.TrimSuffix(strings.TrimSpace(domain), "."),
	}
	v.lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	for _, raw := range staticIPs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		v.staticIPs = append(v.staticIPs, addr)
	}
	return v
}

// NewFromEnv builds a Verifier from DOMAIN and GATEWAY_PUBLIC_IPS
// (comma-separated), the same environment variables the rest of the gateway
// reads its configuration from.
func NewFromEnv() *Verifier {
	var staticIPs []string
	if raw := os.Getenv("GATEWAY_PUBLIC_IPS"); raw != "" {
		staticIPs = strings.Split(raw, ",")
	}
	return New(os.Getenv("DOMAIN"), staticIPs)
}

// Verify reports whether hostname resolves to at least one address this
// gateway answers on. It returns nil on success and a descriptive error
// otherwise, naming both the alias's addresses and the gateway's so an
// operator or the owner of the alias can fix their DNS without guessing.
func (v *Verifier) Verify(ctx context.Context, hostname string) error {
	gatewayIPs, err := v.gatewayAddrs(ctx)
	if err != nil {
		return fmt.Errorf("cannot verify aliases because this gateway does not know its own address: %w", err)
	}

	aliasIPs, err := v.lookupIP(ctx, hostname)
	if err != nil {
		return fmt.Errorf("%s does not resolve yet (DNS may still be propagating): %w", hostname, err)
	}
	if len(aliasIPs) == 0 {
		return fmt.Errorf("%s does not resolve yet (DNS may still be propagating)", hostname)
	}

	aliasAddrs := make([]netip.Addr, 0, len(aliasIPs))
	for _, ip := range aliasIPs {
		if addr, ok := toAddr(ip); ok {
			aliasAddrs = append(aliasAddrs, addr)
		}
	}
	// An answer we could not parse tells us nothing about where the hostname
	// points, so it must not carry ErrPointsElsewhere — falling through to the
	// mismatch below would make a periodic re-check take the domain out of
	// service over an answer it simply failed to read.
	if len(aliasAddrs) == 0 {
		return fmt.Errorf("%s resolved to no usable addresses", hostname)
	}

	for _, a := range aliasAddrs {
		for _, g := range gatewayIPs {
			if a == g {
				return nil
			}
		}
	}

	// The lookup answered, and the answer was not us. This is the one outcome a
	// periodic re-check may act on, so it carries ErrPointsElsewhere.
	return fmt.Errorf("%w: %s resolves to %s, but this gateway answers on %s — point the record at %s",
		ErrPointsElsewhere, hostname, joinAddrs(aliasAddrs), joinAddrs(gatewayIPs), v.domain)
}

// gatewayAddrs returns the address set this gateway answers on, preferring
// the static override and otherwise resolving domain with caching per
// gatewayIPTTL.
func (v *Verifier) gatewayAddrs(ctx context.Context) ([]netip.Addr, error) {
	if len(v.staticIPs) > 0 {
		return v.staticIPs, nil
	}

	if v.domain == "" {
		return nil, fmt.Errorf("DOMAIN is not configured and GATEWAY_PUBLIC_IPS is empty")
	}

	// The lookup runs under the lock on purpose: a burst of verifications then
	// waits on one resolution instead of issuing one lookup each.
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.cachedAt.IsZero() && time.Since(v.cachedAt) < gatewayIPTTL {
		return v.cachedIPs, nil
	}

	ips, err := v.lookupIP(ctx, v.domain)
	if err != nil {
		// A failed lookup is deliberately not remembered. It is usually
		// transient, and caching it would leave every owner unable to verify a
		// hostname for the rest of the TTL after a single DNS hiccup.
		return nil, fmt.Errorf("resolving %s: %w", v.domain, err)
	}

	addrs := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := toAddr(ip); ok {
			addrs = append(addrs, addr)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s resolved to no usable addresses", v.domain)
	}

	v.cachedIPs = addrs
	v.cachedAt = time.Now()
	return addrs, nil
}

// toAddr normalises a net.IP into a netip.Addr in its 4-byte form when
// possible, so a resolver that hands back an IPv4 address wrapped as
// IPv4-in-IPv6 still compares equal to the same address in its plain IPv4
// form. Comparing raw net.IP byte slices (or their .String() output without
// this normalisation) would treat those as different addresses.
func toAddr(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// joinAddrs renders an address list for error messages.
func joinAddrs(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return "no addresses"
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

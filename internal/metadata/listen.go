package metadata

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// The listener's timeouts are deliberately tight. Every caller is a process one hop
// away on a host bridge asking for a handful of bytes, so a request that takes
// seconds is not a slow client but a stuck one, and the service must not be
// holdable by it.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 30 * time.Second
)

// Listen opens the metadata listener.
//
// The socket is opened with IP_FREEBIND on Linux, which is what makes the
// gateway's start independent of Incus's. The address bound here is the VM
// bridge's own, and the bridge does not exist until Incus has brought its
// network up; without this, a gateway that happened to start first would fail to
// bind and a VM would find no metadata service until the next restart.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	cfg := net.ListenConfig{Control: freebind}
	l, err := cfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metadata: listen on %s: %w", addr, err)
	}
	return l, nil
}

// Start opens the listener and serves the metadata API on it in the background.
//
// The error it returns is for the caller to log and carry on from. A gateway
// without a metadata service still runs VMs, serves the dashboard and proxies
// traffic, so a link-local address that could not be bound must not be the
// reason the whole process exits.
func (s *Service) Start(ctx context.Context, addr string) (*http.Server, error) {
	l, err := Listen(ctx, addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	warnIfAddressIsNotLocal(addr)
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metadata service stopped: %v", err)
		}
	}()
	return srv, nil
}

// warnIfAddressIsNotLocal says so when the listener bound an address the host
// does not have.
//
// IP_FREEBIND is what makes that possible, and it is exactly as useful as it is
// dangerous: it lets the gateway start before Incus has brought the bridge up,
// and it just as happily accepts an address that will never exist — a bridge on
// a different subnet than METADATA_ADDR assumes, say. The redirect then delivers
// to the bridge's real address, nothing is listening there, and every VM gets a
// connection reset while the log says the service is listening. A host that has
// simply not started the bridge yet is the expected case, so this is a warning
// and not a refusal; it is here so the mismatch is visible at all.
func warnIfAddressIsNotLocal(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	wanted, err := netip.ParseAddr(host)
	if err != nil || wanted.IsUnspecified() {
		// A name, or 0.0.0.0: neither is a claim about one interface.
		return
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, a := range addrs {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil && prefix.Addr().Unmap() == wanted.Unmap() {
			return
		}
	}
	log.Printf("metadata service: %s is not an address of this host. "+
		"The listener is open (IP_FREEBIND), but nothing will reach it until the address exists — "+
		"check that METADATA_ADDR names the VM bridge's own address.", host)
}

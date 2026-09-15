// Package metadata serves an EC2-compatible instance metadata service to the
// platform's VMs on the link-local address 169.254.169.254, the address every
// cloud-aware tool already reaches for.
//
// The service has no credentials and no sessions. A request is attributed
// entirely to the address it arrives from, which is why identity is resolved
// against the container runtime's live view of which instance holds that address
// rather than against anything the caller says or anything the platform last
// happened to record. Everything it publishes is therefore readable by every
// process in the VM, so nothing secret may enter an Identity.
package metadata

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"
)

// ErrUnknownCaller means the request arrived from an address the platform cannot
// attribute to one of its VMs. It is deliberately not a "not found": a caller
// that is not a VM learns nothing about which VMs exist.
var ErrUnknownCaller = errors.New("metadata: caller is not a known VM")

// PublicKey is one of the owner's SSH keys, published the way EC2 publishes the
// key a machine was launched with.
type PublicKey struct {
	Name string
	Key  string
}

// Identity is the complete set of facts the service may tell a VM about itself.
//
// Every field here is served unauthenticated to whoever holds the VM's network
// namespace — which is every process in it, including anything the agent
// installs. LLM keys, session tokens and password hashes therefore have no
// place in this struct, and the tree test fails if one appears in a response.
type Identity struct {
	ContainerID string
	Name        string
	IncusName   string
	OwnerID     string
	IPv4        string
	// MAC is the hardware address of the interface IPv4 lives on. It is empty
	// when the runtime could not say, and the keys derived from it are then not
	// published rather than invented.
	MAC              string
	CPULimit         int
	MemoryMB         int
	DiskGB           int
	AppPort          int
	AppPublic        bool
	Nesting          bool
	NestingApplied   bool
	InitialTask      string
	InitialTaskState string
	// Aliases are the owner's own verified hostnames for this VM. Unverified
	// ones are left out: they are not routed, so naming them here would promise
	// an address that does not answer.
	Aliases    []string
	PublicKeys []PublicKey
	CreatedAt  time.Time
}

// clone returns an independent copy, slices included.
//
// A shallow copy would not be one: the caches hand the same Identity to every
// request in a window, and two copies sharing a backing array means an append or
// an in-place edit by one caller's handler is read by the next caller's. Nothing
// writes to these today, which is exactly why the copy has to be deep now rather
// than when something does.
func (i *Identity) clone() *Identity {
	if i == nil {
		return nil
	}
	out := *i
	out.Aliases = slices.Clone(i.Aliases)
	out.PublicKeys = slices.Clone(i.PublicKeys)
	return &out
}

// Resolver answers the only question the service asks about a request: which VM
// is on the other end of the socket.
type Resolver interface {
	Resolve(ctx context.Context, addr netip.Addr) (*Identity, error)
}

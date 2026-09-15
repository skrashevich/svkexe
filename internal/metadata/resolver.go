package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// DefaultResolveTTL is how long the runtime's view of which instance holds which
// address is reused. It is short because the answer is a security boundary — a
// reassigned address must stop answering for its previous holder quickly — and
// not shorter because every miss costs one round trip to Incus per instance.
const DefaultResolveTTL = 5 * time.Second

// maxStaleWindows bounds how long a view the runtime could not refresh is still
// trusted. Serving the last known view through a brief Incus hiccup is better
// than a VM losing its identity; serving it indefinitely is not, because the
// longer it is stale the likelier it is that an address has changed hands and the
// answer is somebody else's. Past this, the caller is told the service cannot
// answer instead of being told something that may be wrong.
const maxStaleWindows = 6

// ErrStaleView means the runtime has been unreachable long enough that its last
// known view can no longer be trusted to attribute an address.
var ErrStaleView = errors.New("metadata: the runtime view is too stale to attribute a caller")

// instanceLister is the slice of the container runtime the resolver needs: the
// live view of which instance currently holds which address.
// runtime.ContainerRuntime satisfies it, and so does a fake in tests.
type instanceLister interface {
	List(ctx context.Context, ownerID string) ([]*runtime.Container, error)
}

// RuntimeResolver attributes a source address to a VM.
//
// The order matters and is the whole point of the type: the container runtime
// decides *which instance* holds the address, and the database is consulted only
// afterwards, to load the row for the instance the runtime named. Going the
// other way — looking the address up in the containers table — would trust
// ip_address, a column refreshed only when something happens to a VM. A VM whose
// address was reassigned after it stopped would then still be found by it, and
// the new holder of that address would be handed somebody else's identity.
//
// The premise underneath all of it is that a source address cannot be forged,
// which on a plain Linux bridge is false: a tenant with root in their own VM can
// add a neighbour's address to their interface and answer ARP for it. That is why
// an instance whose NIC does not have Incus's ipv4_filtering enabled is refused
// outright — see addressIsPinned.
type RuntimeResolver struct {
	rt       instanceLister
	database *db.DB
	ttl      time.Duration
	now      func() time.Time

	// mu guards the cached view and is held across the refresh itself. That
	// serialises concurrent misses, which is exactly what keeps a flood of
	// requests from an unattributable address to one runtime call per window.
	mu       sync.Mutex
	holders  map[netip.Addr]holder
	fetched  time.Time
	loaded   bool
	staleFor int
	// identities caches what the database says about an instance for the same
	// window as the runtime view. Without it every request costs four queries on
	// the pool the dashboard, the API and the SSH gateway share, and a loop
	// inside one VM becomes everyone's outage.
	identities map[string]*Identity
	// warned remembers which instances have already been reported as unfiltered,
	// so a misconfigured deployment produces one line per VM rather than one
	// line per request.
	warned map[string]bool
}

// holder is what the live runtime view says about one address.
type holder struct {
	incusName string
	mac       string
}

// NewRuntimeResolver builds a resolver over a container runtime and the platform
// database. A zero ttl means DefaultResolveTTL; a nil clock means time.Now.
func NewRuntimeResolver(rt instanceLister, database *db.DB, ttl time.Duration, now func() time.Time) *RuntimeResolver {
	if ttl <= 0 {
		ttl = DefaultResolveTTL
	}
	if now == nil {
		now = time.Now
	}
	return &RuntimeResolver{rt: rt, database: database, ttl: ttl, now: now, warned: map[string]bool{}}
}

// Resolve returns the identity of the VM holding addr.
func (r *RuntimeResolver) Resolve(ctx context.Context, addr netip.Addr) (*Identity, error) {
	h, cached, err := r.holderOf(ctx, addr)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		// The runtime's answer for the address is what varies per caller; the
		// database's answer for the instance does not.
		clone := cached.clone()
		clone.IPv4 = addr.Unmap().String()
		clone.MAC = h.mac
		return clone, nil
	}
	id, err := r.identify(h, addr)
	if err != nil {
		return nil, err
	}
	r.remember(h.incusName, id)
	return id, nil
}

// holderOf returns the live runtime's answer for addr, refreshing the cached
// view at most once per TTL window, along with any cached identity for it.
func (r *RuntimeResolver) holderOf(ctx context.Context, addr netip.Addr) (holder, *Identity, error) {
	addr = addr.Unmap()

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.loaded || r.now().Sub(r.fetched) >= r.ttl {
		holders, err := r.snapshot(ctx)
		// A refresh that failed still starts a window. Retrying on every request
		// would turn an Incus outage into a request storm against it, and the
		// previous view is a better answer than none: it was true moments ago.
		r.fetched = r.now()
		switch {
		case err == nil:
			r.holders, r.loaded, r.staleFor = holders, true, 0
			// A fresh view means the database may have moved too.
			r.identities = nil
		case !r.loaded:
			return holder{}, nil, err
		default:
			r.staleFor++
		}
	}

	// Checked outside the refresh, not inside it. A failed refresh has already
	// moved the window on, so a ceiling enforced only where the refresh happens
	// would refuse the one request that crossed it and serve the arbitrarily
	// stale view to every request for the rest of that window.
	if r.staleFor > maxStaleWindows {
		return holder{}, nil, fmt.Errorf("%w: %d windows without a refresh", ErrStaleView, r.staleFor)
	}

	h, ok := r.holders[addr]
	if !ok {
		return holder{}, nil, ErrUnknownCaller
	}
	return h, r.identities[h.incusName], nil
}

// remember caches an instance's database facts for the current window.
func (r *RuntimeResolver) remember(incusName string, id *Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.identities == nil {
		r.identities = map[string]*Identity{}
	}
	r.identities[incusName] = id.clone()
}

// snapshot builds the address → instance view from the runtime.
func (r *RuntimeResolver) snapshot(ctx context.Context) (map[netip.Addr]holder, error) {
	live, err := r.rt.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("metadata: list instances: %w", err)
	}
	holders := make(map[netip.Addr]holder, len(live))
	// An address two instances both claim identifies neither, so it is dropped
	// rather than awarded to whichever the runtime listed first. ambiguous keeps
	// a later duplicate from re-adding one that was already withdrawn.
	ambiguous := map[netip.Addr]bool{}
	for _, inst := range live {
		if inst == nil || inst.IP == "" {
			continue
		}
		addr, err := netip.ParseAddr(inst.IP)
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		if ambiguous[addr] {
			continue
		}
		if !r.addressIsPinned(inst) {
			continue
		}
		if _, taken := holders[addr]; taken {
			delete(holders, addr)
			ambiguous[addr] = true
			continue
		}
		holders[addr] = holder{incusName: inst.ID, mac: inst.MAC}
	}
	return holders, nil
}

// addressIsPinned reports whether the runtime is enforcing that this instance
// can only send from the address it was given.
//
// Without that enforcement the whole service is unsound, not merely imperfect: a
// tenant is root inside their own VM, so they can add a neighbour's address to
// their interface, answer ARP for it, and be served the neighbour's identity,
// SSH keys and task — including an IMDSv2 token bound to the neighbour. There is
// nothing this process can check to tell that apart, because the forgery is in
// the IP header.
//
// So an unpinned instance is not served at all. That fails closed.
//
// One honest limit: what is read here is the instance's expanded *device
// configuration*, which is what the operator asked for, not proof that Incus has
// the filter rules in place. Editing the profile flips this to true for every
// instance inheriting it, running ones included, and Incus normally applies the
// filters to a running instance straight away — but if it cannot (no resolvable
// DHCP lease to pin, say) the configuration still reads as filtered. There is
// nothing better to read: the runtime does not report enforcement separately.
func (r *RuntimeResolver) addressIsPinned(inst *runtime.Container) bool {
	if inst.AddressFiltered {
		return true
	}
	if !r.warned[inst.ID] {
		r.warned[inst.ID] = true
		log.Printf("metadata: refusing to answer %s: one of its NICs has no security.ipv4_filtering, "+
			"so its source address cannot be trusted. Run scripts/install-metadata-units.sh.", inst.ID)
	}
	return false
}

// identify loads what the platform knows about the instance the runtime named.
// An instance with no row of its own — an image build container, say — is not a
// VM of the platform's and is refused like any other unknown caller.
func (r *RuntimeResolver) identify(h holder, addr netip.Addr) (*Identity, error) {
	if r.database == nil {
		return nil, ErrUnknownCaller
	}
	c, err := r.database.GetContainerByIncusName(h.incusName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownCaller
	}
	if err != nil {
		return nil, fmt.Errorf("metadata: load %s: %w", h.incusName, err)
	}

	id := &Identity{
		ContainerID:      c.ID,
		Name:             c.Name,
		IncusName:        c.IncusName,
		OwnerID:          c.OwnerID,
		IPv4:             addr.Unmap().String(),
		MAC:              h.mac,
		CPULimit:         c.CPULimit,
		MemoryMB:         c.MemoryMB,
		DiskGB:           c.DiskGB,
		AppPort:          c.AppPort,
		AppPublic:        c.AppPublic,
		NestingApplied:   c.NestingApplied,
		InitialTask:      c.InitialTask,
		InitialTaskState: c.InitialTaskState,
		CreatedAt:        c.CreatedAt,
	}

	// The VM is told what nesting is actually in effect for it, which is the
	// owner's wish capped by the operator's ceiling — the same resolution the
	// start path applies. A ceiling that cannot be read denies the capability,
	// because claiming it works when it does not sends the agent diagnosing a
	// Docker daemon that was never going to start.
	allowed, err := r.database.NestingAllowed()
	if err != nil {
		allowed = false
	}
	id.Nesting = allowed && c.Nesting

	// The remaining reads only enrich the answer. A VM losing its identity
	// because one of them failed would be worse than a VM told about fewer of
	// its own addresses or keys, so they are best-effort.
	if aliases, err := r.database.ListAliasesByContainer(c.ID); err == nil {
		for _, a := range aliases {
			if a != nil && a.Verified {
				id.Aliases = append(id.Aliases, a.Hostname)
			}
		}
	}
	if keys, err := r.database.ListSSHKeysByUser(c.OwnerID); err == nil {
		for _, k := range keys {
			if k != nil {
				id.PublicKeys = append(id.PublicKeys, PublicKey{Name: k.Name, Key: k.PublicKey})
			}
		}
	}
	return id, nil
}

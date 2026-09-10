// Package aliases manages the custom hostnames an owner points at a VM.
//
// Adding an alias is not a single write: the name has to be validated against
// the gateway's own namespace, claimed exclusively, checked against DNS, and
// then announced to the agent running in the VM. The REST API and the
// dashboard both offer the operation, so the sequence lives here rather than
// twice in two handlers that would drift apart.
package aliases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/dnscheck"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// Verifier confirms that a hostname resolves to this gateway.
// *dnscheck.Verifier implements it; tests substitute their own.
type Verifier interface {
	Verify(ctx context.Context, hostname string) error
}

var (
	// ErrInvalid means the hostname itself is unusable, which the caller can
	// only fix by supplying a different one.
	ErrInvalid = errors.New("invalid hostname")
	// ErrNotFound means the alias does not exist, or belongs to another VM.
	ErrNotFound = errors.New("alias not found")
	// ErrNoVerifier means the deployment cannot check DNS at all, so no alias
	// can ever become routable. Surfacing it beats silently leaving every
	// alias stuck as "pending" with no explanation.
	ErrNoVerifier = errors.New("alias verification is not configured on this gateway")
)

// Manager creates, re-checks and removes VM aliases.
type Manager struct {
	db *db.DB
	// runtime may be nil, in which case the agent simply is not told about the
	// change and picks it up the next time its VM starts.
	rt runtime.ContainerRuntime
	// verifier may be nil in deployments that cannot resolve DNS; aliases can
	// still be created, but they stay unverified and therefore unrouted.
	verifier Verifier
	// domain is the gateway's own base domain, which no alias may fall under.
	domain string
}

// New builds a Manager.
func New(database *db.DB, rt runtime.ContainerRuntime, verifier Verifier, domain string) *Manager {
	return &Manager{db: database, rt: rt, verifier: verifier, domain: domain}
}

// List returns a container's aliases.
func (m *Manager) List(containerID string) ([]*db.ContainerAlias, error) {
	return m.db.ListAliasesByContainer(containerID)
}

// Add claims a hostname for a container and immediately checks whether it
// points here. A failed check is not a failed request: the row is kept with the
// reason recorded, so the owner can fix their DNS and re-check rather than
// having to retype the hostname.
func (m *Manager) Add(ctx context.Context, c *db.Container, hostname string) (*db.ContainerAlias, error) {
	normalised, err := db.ValidAliasHostname(hostname, m.domain)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err)
	}

	alias, err := m.db.CreateContainerAlias(c.ID, normalised)
	if err != nil {
		return nil, err
	}

	return m.check(ctx, c, alias)
}

// Recheck re-runs the DNS check for an existing alias, which is how an owner
// confirms a record they have just fixed — and how a name that stopped pointing
// here stops being routed.
func (m *Manager) Recheck(ctx context.Context, c *db.Container, aliasID string) (*db.ContainerAlias, error) {
	alias, err := m.owned(c, aliasID)
	if err != nil {
		return nil, err
	}
	return m.check(ctx, c, alias)
}

// Remove releases a hostname, making it claimable again.
func (m *Manager) Remove(ctx context.Context, c *db.Container, aliasID string) error {
	alias, err := m.owned(c, aliasID)
	if err != nil {
		return err
	}
	if err := m.db.DeleteContainerAlias(alias.ID); err != nil {
		return err
	}
	m.refreshGuide(ctx, c)
	return nil
}

// Release frees a hostname platform-wide, deleting every VM's claim on it, and
// reports how many it removed.
//
// It is the operator's answer to a name being held by the wrong account.
// Verification proves that a hostname points at THIS GATEWAY, not who controls
// it, so on a shared gateway the first tenant to verify a name keeps it — and
// between the moment the real owner points their DNS here and the moment they
// press Add, anyone who knows the name can take it. Without this the only way
// to undo that would be deleting the holder's VM or their whole account.
//
// Caller must have already established that this is an administrator.
func (m *Manager) Release(ctx context.Context, hostname string) (int64, error) {
	// The claims are read before the delete so the agents that were told about
	// this address can be corrected afterwards.
	claims, err := m.db.ListAliasesByHostname(hostname)
	if err != nil {
		return 0, err
	}
	if len(claims) == 0 {
		return 0, ErrNotFound
	}

	removed, err := m.db.DeleteAliasesByHostname(hostname)
	if err != nil {
		return 0, err
	}

	seen := make(map[string]bool, len(claims))
	for _, claim := range claims {
		if seen[claim.ContainerID] {
			continue
		}
		seen[claim.ContainerID] = true
		c, err := m.db.GetContainerByID(claim.ContainerID)
		if err != nil {
			log.Printf("release %s: reload VM %s: %v", claim.Hostname, claim.ContainerID, err)
			continue
		}
		m.refreshGuide(ctx, c)
	}
	return removed, nil
}

// owned resolves an alias ID within a container, so an ID guessed from another
// VM cannot be re-checked or deleted through this VM's routes.
func (m *Manager) owned(c *db.Container, aliasID string) (*db.ContainerAlias, error) {
	alias, err := m.db.GetAliasByID(aliasID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if alias.ContainerID != c.ID {
		return nil, ErrNotFound
	}
	return alias, nil
}

// check runs the DNS check and stores its outcome, then re-reads the alias so
// the caller reports what was actually persisted.
func (m *Manager) check(ctx context.Context, c *db.Container, alias *db.ContainerAlias) (*db.ContainerAlias, error) {
	verified := false
	reason := ErrNoVerifier.Error()
	if m.verifier != nil {
		if err := m.verifier.Verify(ctx, alias.Hostname); err != nil {
			reason = err.Error()
		} else {
			verified = true
			reason = ""
		}
	}

	if err := m.db.SetAliasVerification(alias.ID, verified, reason); err != nil {
		// Another VM verified the same hostname first. That is the owner's
		// answer, not a server fault, so it is recorded on the alias the same
		// way a failed DNS check is and the row stays for them to see.
		if !errors.Is(err, db.ErrAliasHostnameTaken) {
			return nil, err
		}
		if err := m.db.SetAliasVerification(alias.ID, false, db.ErrAliasHostnameTaken.Error()); err != nil {
			return nil, err
		}
	}
	// Only a verified alias is an address the agent can promise, so the guide
	// is worth rewriting exactly when the verified set may have changed.
	m.refreshGuide(ctx, c)

	updated, err := m.db.GetAliasByID(alias.ID)
	// Guard against a race, not a path anything exercises: another VM's
	// verification can delete this competing claim between the write above and
	// this read. The alias is gone, not broken, so the caller gets a 404 rather
	// than a server error.
	//
	// Deliberately untested. Reproducing it would need a seam in this function
	// between the write and the read — a test hook in production code bought
	// for a cosmetic status code. Do not read this branch as verified
	// behaviour, and do not build on it.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return updated, err
}

// ReverifyInterval is how often routed custom domains are re-checked. Domains
// change hands on the scale of days, so checking hourly is frequent enough to
// bound the damage while costing one DNS lookup per alias.
const ReverifyInterval = time.Hour

// Reverify periodically re-checks every routed custom domain and stops routing
// the ones that have stopped pointing here.
//
// It exists because a verified alias would otherwise stay verified forever. An
// owner who lets a domain go leaves a row behind that keeps answering both the
// routing lookup and the certificate check; when somebody else buys that domain
// and points it at this gateway — exactly what the documentation tells them to
// do — their visitors would land in the previous owner's VM, under a
// certificate this gateway happily renews.
//
// A domain is only demoted on dnscheck.ErrPointsElsewhere, meaning DNS answered
// and the answer was somebody else. A lookup that fails, or a gateway that
// cannot resolve its own address, leaves every alias exactly as it was: those
// say nothing about who owns the name, and acting on them would take working
// domains offline in a batch during any DNS trouble.
func Reverify(ctx context.Context, database *db.DB, rt runtime.ContainerRuntime, verifier Verifier, interval time.Duration) {
	if verifier == nil || interval <= 0 {
		return
	}
	// One sweep shortly after startup. A ticker alone would mean a deployment
	// that restarts more often than the interval never sweeps at all, and this
	// gateway updates and restarts itself. The jitter keeps a fleet restarting
	// together from turning into one simultaneous burst of DNS lookups.
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupDelay()):
	}
	reverifyOnce(ctx, database, rt, verifier)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reverifyOnce(ctx, database, rt, verifier)
		}
	}
}

// ReverifyStartupJitter bounds how long the first sweep waits after startup.
// It is a variable rather than a constant so tests can drive the loop without
// waiting out a real delay.
var ReverifyStartupJitter = 30 * time.Second

func startupDelay() time.Duration {
	if ReverifyStartupJitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(ReverifyStartupJitter)))
}

// reverifyOnce runs a single sweep. It is separate from the loop so a test can
// drive it without waiting on a ticker.
func reverifyOnce(ctx context.Context, database *db.DB, rt runtime.ContainerRuntime, verifier Verifier) {
	aliases, err := database.ListVerifiedAliases()
	if err != nil {
		log.Printf("alias re-check: list verified domains: %v", err)
		return
	}
	for _, a := range aliases {
		if ctx.Err() != nil {
			return
		}
		err := verifier.Verify(ctx, a.Hostname)
		if err == nil || !errors.Is(err, dnscheck.ErrPointsElsewhere) {
			continue
		}
		if err := database.SetAliasVerification(a.ID, false, err.Error()); err != nil {
			log.Printf("alias re-check: stop routing %s: %v", a.Hostname, err)
			continue
		}
		log.Printf("alias re-check: %s no longer points here, routing stopped", a.Hostname)
		// The agent was told this address serves its work. Leaving it in the
		// guide would have it keep reporting a URL that now answers 404.
		if rt == nil {
			continue
		}
		c, err := database.GetContainerByID(a.ContainerID)
		if err != nil {
			log.Printf("alias re-check: reload VM for %s: %v", a.Hostname, err)
			continue
		}
		if err := picoclaw.RefreshEnvironmentGuide(ctx, rt, database, c); err != nil {
			log.Printf("alias re-check: refresh agent guide for %s: %v", c.IncusName, err)
		}
	}
}

// refreshGuide tells the agent which addresses its VM now answers on. The guide
// loads the current domain list itself, so this does not have to hand it one.
// Not reaching the VM must not fail the change the owner just made — a stopped
// VM picks it up when it next starts.
func (m *Manager) refreshGuide(ctx context.Context, c *db.Container) {
	if m.rt == nil {
		return
	}
	if err := picoclaw.RefreshEnvironmentGuide(ctx, m.rt, m.db, c); err != nil {
		log.Printf("aliases for %s: refresh agent guide: %v", c.IncusName, err)
	}
}

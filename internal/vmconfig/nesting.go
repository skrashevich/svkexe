// Package vmconfig applies the platform's per-VM runtime settings to the
// container runtime. It sits between the database, which stores what an owner
// and an operator asked for, and the runtime, which can only be told a single
// resolved answer.
package vmconfig

import (
	"context"
	"errors"
	"fmt"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// EffectiveNesting resolves the owner's wish against the deployment-wide
// ceiling. It reads the ceiling itself so that callers cannot forget it: an
// operator who turned nesting off platform-wide must not be overridden by a row
// that still says the owner wants it.
//
// A ceiling that cannot be read resolves to false, and the error is returned
// alongside it. Both halves matter: the value is what callers must act on, so
// an unreadable policy denies the capability rather than granting it, while the
// error is what tells the caller this was a failure and not a decision.
func EffectiveNesting(database *db.DB, c *db.Container) (bool, error) {
	if database == nil || c == nil {
		return false, nil
	}
	allowed, err := database.NestingAllowed()
	if err != nil {
		return false, fmt.Errorf("resolve the nesting policy: %w", err)
	}
	return allowed && c.Nesting, nil
}

// ApplyNesting writes the resolved setting onto the instance. It is what a
// settings change calls: the value reaches Incus immediately, but nesting_applied
// is left untouched for a running VM, because LXC only reads the key at boot and
// the dashboard has to keep showing that a restart is owed.
//
// A stopped VM is recorded as applied right away — there is nothing left to owe,
// since its next start already picks the new value up.
func ApplyNesting(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if rt == nil || database == nil || c == nil {
		return nil
	}
	// A failure to resolve the policy does not stop the write. Returning here
	// would leave the instance on whatever it was last configured with, which is
	// exactly the stale value an operator may have just revoked; writing the
	// denied value instead is both safe and self-healing, since the next start
	// resolves the policy again.
	effective, resolveErr := EffectiveNesting(database, c)
	if err := rt.SetNesting(ctx, c.IncusName, effective); err != nil {
		return errors.Join(resolveErr, fmt.Errorf("apply nesting to %s: %w", c.IncusName, err))
	}
	if !isRunning(c.Status) {
		if err := database.SetNestingApplied(c.ID, effective); err != nil {
			return errors.Join(resolveErr, err)
		}
		c.NestingApplied = effective
	}
	return resolveErr
}

// PrepareStart writes the resolved setting and records it as the one in effect.
// Every path that boots a container calls it just before the start, which is
// what keeps "nesting is live here" honest no matter which entry point — the
// dashboard, the REST API or the SSH menu — did the starting.
func PrepareStart(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if rt == nil || database == nil || c == nil {
		return nil
	}
	// As in ApplyNesting, an unresolvable policy is written as a denial rather
	// than skipped: every caller here logs the error and starts the VM anyway,
	// so returning early would boot it on a stale instance config. The error
	// still travels back, which is what distinguishes this from a decision.
	effective, resolveErr := EffectiveNesting(database, c)
	if err := rt.SetNesting(ctx, c.IncusName, effective); err != nil {
		return errors.Join(resolveErr, fmt.Errorf("apply nesting to %s: %w", c.IncusName, err))
	}
	if err := database.SetNestingApplied(c.ID, effective); err != nil {
		return errors.Join(resolveErr, err)
	}
	c.NestingApplied = effective
	return resolveErr
}

// MarkStarted records the setting a freshly created instance booted with. It
// exists for the create path, where the value already travelled in CreateOpts
// and writing it again through the runtime would be a wasted round trip.
func MarkStarted(database *db.DB, c *db.Container, effective bool) error {
	if database == nil || c == nil {
		return nil
	}
	if err := database.SetNestingApplied(c.ID, effective); err != nil {
		return err
	}
	c.NestingApplied = effective
	return nil
}

func isRunning(status string) bool {
	return status == "running" || status == "Running" || status == "RUNNING"
}

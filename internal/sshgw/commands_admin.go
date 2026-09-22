package sshgw

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// The administrator surface is one command with actions rather than seven
// top-level words: everything here crosses account boundaries, and keeping that
// behind a single "admin" prefix means a tenant reading the help never has to
// wonder which of the listed commands would have touched somebody else's VMs.
//
// There is no bare form — "admin" alone is not an operation — so Run is left
// nil and the dispatcher answers the word on its own by listing the actions.
func adminCommands() []*command {
	return []*command{
		{
			Name:      "admin",
			Group:     groupAdmin,
			AdminOnly: true,
			Usage:     "admin <users|rmuser|vms|nesting|domains|release|update> [...]",
			Summary:   "Platform-wide operator actions",
			Description: "Reaches across every account: the user list, every VM on the host, the deployment-wide nesting ceiling, every custom domain, and this gateway's own updates. " +
				"It is the same surface as the dashboard's admin pages.",
			Subcommands: []subSpec{
				{
					Name:  "users",
					Usage: "admin users [--json]",
					Desc:  "Every account with its role, VM count and creation date",
					JSON:  true,
					Run:   cmdAdminUsers,
				},
				{
					Name:  "rmuser",
					Usage: "admin rmuser <id|email> [--force]",
					Desc:  "Delete an account and every record the gateway keeps for it",
					Args:  []argSpec{{Name: "account", Desc: "the account to delete, by id or email"}},
					Flags: []flagSpec{{Name: "force", Desc: "required to delete from a one-shot invocation"}},
					Run:   cmdAdminRemoveUser,
				},
				{
					Name:  "vms",
					Usage: "admin vms [--json]",
					Desc:  "Every VM on the platform with the account that owns it",
					JSON:  true,
					Run:   cmdAdminVMs,
				},
				{
					Name:  "nesting",
					Usage: "admin nesting [on|off]",
					Desc:  "Report or set the deployment-wide ceiling on nested containers",
					Args:  []argSpec{{Name: "state", Desc: "on or off; omit it to report the current ceiling", Optional: true}},
					Run:   cmdAdminNesting,
				},
				{
					Name:  "domains",
					Usage: "admin domains [--json]",
					Desc:  "Every custom domain claimed on the platform, with its VM and owner",
					JSON:  true,
					Run:   cmdAdminDomains,
				},
				{
					Name:  "release",
					Usage: "admin release <hostname>",
					Desc:  "Free a hostname platform-wide so another account can claim it",
					Args:  []argSpec{{Name: "hostname", Desc: "the custom domain to free"}},
					Run:   cmdAdminRelease,
				},
				{
					Name:  "update",
					Usage: "admin update [status|check|start] [--force] [--json]",
					Desc:  "The running build, what is available upstream, and the update itself",
					Args:  []argSpec{{Name: "action", Desc: "status (the default), check or start", Optional: true}},
					Flags: []flagSpec{{Name: "force", Desc: "for \"check\": ask GitHub again instead of reusing the cached answer"}},
					JSON:  true,
					Run:   cmdAdminUpdate,
				},
			},
			Examples: []string{
				"admin users --json",
				"admin vms",
				"admin nesting off",
				"admin rmuser someone@example.com",
				"admin release app.example.com",
				"admin update check --force",
			},
		},
	}
}

// --- Accounts ---

// adminUserJSON is one account as an operator sees it. The password hash and
// the stored model are deliberately absent: this is a roster, not an export.
type adminUserJSON struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	VMs       int    `json:"vms"`
	CreatedAt string `json:"created_at"`
}

func cmdAdminUsers(c *cmdCtx) error {
	users, err := c.s.db.ListUsers()
	if err != nil {
		return err
	}
	// One pass over every container rather than a query per account: the VM
	// count is the only thing the roster needs from that table.
	counts, err := c.s.vmsPerOwner()
	if err != nil {
		return err
	}

	if c.json {
		views := make([]adminUserJSON, 0, len(users))
		for _, u := range users {
			views = append(views, adminUserJSON{
				ID:        u.ID,
				Email:     u.Email,
				Role:      u.Role,
				VMs:       counts[u.ID],
				CreatedAt: formatTime(u.CreatedAt),
			})
		}
		return c.writeJSON(views)
	}

	if len(users) == 0 {
		c.print("No accounts exist.\n")
		return nil
	}
	c.print("\n")
	c.printf("  %-38s %-32s %-6s %4s  %s\n", "ID", "EMAIL", "ROLE", "VMS", "CREATED")
	c.printf("  %-38s %-32s %-6s %4s  %s\n", "--", "-----", "----", "---", "-------")
	for _, u := range users {
		c.printf("  %-38s %-32s %-6s %4d  %s\n",
			u.ID, u.Email, u.Role, counts[u.ID], u.CreatedAt.Format("2006-01-02"))
	}
	c.print("\n")
	return nil
}

func cmdAdminRemoveUser(c *cmdCtx) error {
	target, err := c.adminFindUser(c.arg(0))
	if err != nil {
		return err
	}
	// An admin deleting their own account would take their own session's
	// identity with it, and on a single-admin deployment nobody would be left
	// who could undo it. Another administrator has to do it.
	if target.ID == c.user.ID {
		return fmt.Errorf("refusing to delete your own account (%s); another administrator has to do that", target.Email)
	}
	if err := c.confirmDestructive(fmt.Sprintf("deleting the account %s", target.Email)); err != nil {
		return err
	}

	containers, err := c.s.db.ListContainersByOwner(target.ID)
	if err != nil {
		return err
	}
	if err := c.s.db.DeleteUser(target.ID); err != nil {
		return err
	}

	c.printf("Account %s (%s) deleted.\n", target.Email, target.ID)
	c.printf("It cascaded: %d VM record(s), their custom domains, their API keys and every shared link they created or that pointed at their VMs.\n", len(containers))
	// This is the half an operator has to know before typing the command: the
	// delete is a database transaction, so the instances keep running, keep
	// their disks and keep their names — which still encode the now-deleted
	// owner's ID. Deleting the VMs first is the only way to reclaim that space.
	c.print("Their Incus containers are NOT deleted: they keep running on the host with their disks intact. Delete the VMs first if the instances should go too.\n")
	return nil
}

// adminFindUser resolves the one argument rmuser takes, which is an ID or an
// email because an operator reading either the roster or a support ticket
// should not have to translate between them.
func (c *cmdCtx) adminFindUser(ref string) (*db.User, error) {
	if ref == "" {
		return nil, usagef("name the account to delete, by id or email")
	}
	user, err := c.s.db.GetUserByID(ref)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	user, err = c.s.db.GetUserByEmail(ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no account with id or email %q", ref)
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// --- VMs across owners ---

// adminVMJSON is the ordinary VM view with the owner attached, so a program
// parsing "admin vms" reads the same field names as "ls --json" and only has to
// learn the two extra ones.
type adminVMJSON struct {
	vmJSON
	OwnerID    string `json:"owner_id"`
	OwnerEmail string `json:"owner_email"`
}

func cmdAdminVMs(c *cmdCtx) error {
	containers, err := c.s.db.ListAllContainers()
	if err != nil {
		return err
	}
	// The ceiling is one setting for the whole deployment, so attaching it here
	// makes the nesting fields mean the same thing they do in "stat".
	if err := c.s.db.AttachNestingPolicy(containers...); err != nil {
		return err
	}
	emails, err := c.s.ownerEmails()
	if err != nil {
		return err
	}

	if c.json {
		views := make([]adminVMJSON, 0, len(containers))
		for _, container := range containers {
			views = append(views, adminVMJSON{
				vmJSON:     newVMJSON(container, c.s.domain),
				OwnerID:    container.OwnerID,
				OwnerEmail: emails[container.OwnerID],
			})
		}
		return c.writeJSON(views)
	}

	if len(containers) == 0 {
		c.print("No VMs exist on this platform.\n")
		return nil
	}
	c.print("\n")
	c.printf("  %-20s %-32s %-10s %-6s %-8s %-6s\n", "NAME", "OWNER", "STATUS", "CPU", "MEMORY", "DISK")
	c.printf("  %-20s %-32s %-10s %-6s %-8s %-6s\n", "----", "-----", "------", "---", "------", "----")
	for _, container := range containers {
		c.printf("  %-20s %-32s %s %-7s %-6d %-8s %-6s\n",
			container.Name,
			adminOwnerLabel(emails, container.OwnerID),
			statusIcon(container.Status),
			container.Status,
			container.CPULimit,
			formatMB(container.MemoryMB),
			formatGB(container.DiskGB),
		)
	}
	c.print("\n")
	return nil
}

// adminOwnerLabel falls back to the raw owner ID: a VM whose account row has
// gone is exactly the case an operator is looking for in this table, so it must
// not render as an empty column.
func adminOwnerLabel(emails map[string]string, ownerID string) string {
	if email := emails[ownerID]; email != "" {
		return email
	}
	return ownerID + " (no account)"
}

// ownerEmails maps every account ID to its email in one query.
func (s *Server) ownerEmails() (map[string]string, error) {
	users, err := s.db.ListUsers()
	if err != nil {
		return nil, err
	}
	emails := make(map[string]string, len(users))
	for _, u := range users {
		emails[u.ID] = u.Email
	}
	return emails, nil
}

// vmsPerOwner counts the VMs each account holds.
func (s *Server) vmsPerOwner() (map[string]int, error) {
	containers, err := s.db.ListAllContainers()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, container := range containers {
		counts[container.OwnerID]++
	}
	return counts, nil
}

// --- The nesting ceiling ---

func cmdAdminNesting(c *cmdCtx) error {
	if state := c.arg(0); state != "" {
		allowed, err := parseOnOff(state)
		if err != nil {
			return err
		}
		if err := c.s.db.SetNestingAllowed(allowed); err != nil {
			return err
		}
	}

	// Reported from the stored setting rather than from what was just written,
	// so the answer is what a VM will actually boot against.
	allowed, err := c.s.db.NestingAllowed()
	if err != nil {
		return err
	}
	if allowed {
		c.print("Nested containers are allowed: a VM whose owner asks for nesting boots with it.\n")
	} else {
		c.print("Nested containers are forbidden: no VM may boot with nesting, whatever its owner asked for.\n")
	}
	// The ceiling is applied when an instance starts, and rewriting every
	// running VM from one command would take the platform down to enforce a
	// policy that takes effect on the next start anyway.
	c.print("This changes what VMs may boot with. A running VM keeps the nesting it booted with until it restarts.\n")
	return nil
}

// --- Custom domains ---

// adminAliasJSON is one hostname claim with the VM and account behind it.
type adminAliasJSON struct {
	Hostname   string `json:"hostname"`
	Verified   bool   `json:"verified"`
	VM         string `json:"vm"`
	OwnerID    string `json:"owner_id"`
	OwnerEmail string `json:"owner_email"`
	LastError  string `json:"last_error,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func cmdAdminDomains(c *cmdCtx) error {
	// The rows arrive grouped by hostname with the verified claim first, which
	// is the order a dispute is read in: the holder, then everyone who wanted
	// the same name.
	list, err := c.s.db.ListAllAliases()
	if err != nil {
		return err
	}

	if c.json {
		views := make([]adminAliasJSON, 0, len(list))
		for _, a := range list {
			views = append(views, adminAliasJSON{
				Hostname:   a.Hostname,
				Verified:   a.Verified,
				VM:         a.ContainerName,
				OwnerID:    a.OwnerID,
				OwnerEmail: a.OwnerEmail,
				LastError:  a.LastError,
				CreatedAt:  formatTime(a.CreatedAt),
			})
		}
		return c.writeJSON(views)
	}

	if len(list) == 0 {
		c.print("No custom domains are claimed on this platform.\n")
		return nil
	}
	c.print("\n")
	c.printf("  %-40s %-20s %-32s %s\n", "HOSTNAME", "VM", "OWNER", "STATE")
	c.printf("  %-40s %-20s %-32s %s\n", "--------", "--", "-----", "-----")
	for _, a := range list {
		c.printf("  %-40s %-20s %-32s %s\n",
			a.Hostname, a.ContainerName, a.OwnerEmail, aliasState(&a.ContainerAlias))
	}
	c.print("\n")
	return nil
}

func cmdAdminRelease(c *cmdCtx) error {
	hostname := c.arg(0)
	if hostname == "" {
		return usagef("name the hostname to release")
	}
	if c.s.aliases == nil {
		return errors.New("custom domains are not configured in this deployment, so there is no claim to release")
	}

	removed, err := c.s.aliases.Release(c.ctx, hostname)
	if errors.Is(err, aliases.ErrNotFound) {
		return fmt.Errorf("no VM claims %q", hostname)
	}
	if err != nil {
		return err
	}
	// Taking a domain away is an operator overriding a tenant, so it leaves a
	// trace naming who did it, exactly as the REST route does.
	log.Printf("ssh admin %s released hostname %q (%d claims removed)", c.user.Email, hostname, removed)

	c.printf("Released %q: %d claim(s) removed.\n", hostname, removed)
	c.print("Any account may now claim it again; the agents that were told about the address have been updated.\n")
	return nil
}

// --- Self-update ---

// errUpdaterDisabled is what every update action answers on a deployment built
// without the self-update wiring. That is a supported deployment rather than a
// bug, and refusing loses nothing an operator needs: the running build is
// already reported by "help --json" and by the greeting.
var errUpdaterDisabled = errors.New("self-update is not configured in this deployment")

// adminUpdateJSON is the whole update picture: what is running, whether this
// host can replace it, what is upstream, and how the last attempt went.
type adminUpdateJSON struct {
	Version           version.Info     `json:"version"`
	CanUpdate         bool             `json:"can_update"`
	UnavailableReason string           `json:"unavailable_reason,omitempty"`
	Run               updater.RunState `json:"run"`
	Check             *updater.Status  `json:"check,omitempty"`
}

func cmdAdminUpdate(c *cmdCtx) error {
	if c.s.updater == nil {
		return errUpdaterDisabled
	}
	switch action := c.arg(0); action {
	case "", "status":
		return cmdAdminUpdateStatus(c)
	case "check":
		return cmdAdminUpdateCheck(c)
	case "start":
		return cmdAdminUpdateStart(c)
	default:
		return usagef("unknown update action %q; try status, check or start", action)
	}
}

// adminUpdateState reads the progress of the last run. An unreadable status
// file says nothing about the update itself, so it is surfaced as a failed run
// carrying the read error rather than swallowed into "idle".
func adminUpdateState(c *cmdCtx) updater.RunState {
	state, err := c.s.updater.State()
	if err != nil {
		return updater.RunState{State: updater.StateFailed, Error: err.Error()}
	}
	return state
}

func cmdAdminUpdateStatus(c *cmdCtx) error {
	can, reason := c.s.updater.Available()
	state := adminUpdateState(c)

	if c.json {
		return c.writeJSON(adminUpdateJSON{
			Version:           c.s.updater.Version(),
			CanUpdate:         can,
			UnavailableReason: reason,
			Run:               state,
		})
	}
	c.print("\n")
	c.printf("  Build:       %s\n", c.s.updater.Version().Short())
	if can {
		c.print("  Self-update: available\n")
	} else {
		c.printf("  Self-update: unavailable (%s)\n", reason)
	}
	c.printf("  Last run:    %s\n", adminRunSummary(state))
	c.print("\n")
	return nil
}

func cmdAdminUpdateCheck(c *cmdCtx) error {
	// The check is cached so the dashboard can poll it cheaply; --force is how
	// an operator who just cut a release asks GitHub again rather than being
	// handed a fifteen-minute-old answer that makes the gateway look stuck.
	status, err := c.s.updater.Check(c.ctx, c.hasFlag("force"))
	if err != nil {
		return fmt.Errorf("update check: %w", err)
	}
	can, reason := c.s.updater.Available()

	if c.json {
		return c.writeJSON(adminUpdateJSON{
			Version:           c.s.updater.Version(),
			CanUpdate:         can,
			UnavailableReason: reason,
			Run:               adminUpdateState(c),
			Check:             &status,
		})
	}
	c.print("\n")
	c.printf("  Running:  %s\n", status.Current.Short())
	if status.Latest != nil {
		c.printf("  Upstream: %s (%s, %s)\n", status.Latest.Version, status.Latest.Channel, status.Latest.PublishedAt.UTC().Format("2006-01-02 15:04"))
		if status.Latest.URL != "" {
			c.printf("            %s\n", status.Latest.URL)
		}
	}
	switch {
	case status.Reason != "":
		// A build with no version or commit stamp has nothing to compare, which
		// is a statement about this binary rather than about upstream.
		c.printf("  No comparison: %s\n", status.Reason)
	case status.UpdateAvailable:
		c.print("  An update is available; run \"admin update start\" to install it.\n")
	default:
		c.print("  This gateway is up to date.\n")
	}
	if !can {
		c.printf("  Note: this host cannot install it (%s).\n", reason)
	}
	c.print("\n")
	return nil
}

func cmdAdminUpdateStart(c *cmdCtx) error {
	// An update restarts the whole platform and every VM session riding on it,
	// so record who asked for it before anything happens.
	log.Printf("ssh admin update requested by user %s (%s)", c.user.ID, c.user.Email)

	// Both refusals — a run already in flight, and a host with no way to
	// install anything — are returned as they are: the runner's message names
	// the reason, and neither is a failure of this command.
	if err := c.s.updater.Start(c.ctx); err != nil {
		return err
	}
	// Start seeds a running state on disk, so re-reading it reports the run
	// that was just placed instead of a stale idle.
	state := adminUpdateState(c)

	if c.json {
		can, reason := c.s.updater.Available()
		return c.writeJSON(adminUpdateJSON{
			Version:           c.s.updater.Version(),
			CanUpdate:         can,
			UnavailableReason: reason,
			Run:               state,
		})
	}
	c.print("Update requested. The gateway restarts itself as part of it, so this session will drop.\n")
	c.printf("State: %s\n", adminRunSummary(state))
	return nil
}

// adminRunSummary renders one update run as a single line.
func adminRunSummary(state updater.RunState) string {
	var b strings.Builder
	b.WriteString(state.State)
	if !state.StartedAt.IsZero() {
		b.WriteString(", started " + state.StartedAt.UTC().Format("2006-01-02 15:04"))
	}
	if !state.FinishedAt.IsZero() {
		b.WriteString(", finished " + state.FinishedAt.UTC().Format("2006-01-02 15:04"))
	}
	if state.Commit != "" {
		b.WriteString(", commit " + state.Commit)
	}
	if state.Error != "" {
		b.WriteString(": " + state.Error)
	}
	return b.String()
}

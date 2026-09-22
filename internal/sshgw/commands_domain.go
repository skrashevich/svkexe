package sshgw

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/db"
)

// errNoAliasManager is what every "domain" action fails with on a deployment
// that has no base domain: the alias manager is nil there, and a gateway that
// does not route anything by name has nothing for a custom name to be pointed
// at either.
var errNoAliasManager = errors.New("custom domains are not configured on this gateway (it has no DOMAIN set)")

func domainCommands() []*command {
	return []*command{
		{
			Name:        "domain",
			Group:       groupDomains,
			Usage:       "domain <list|add|verify|rm> <vm> [hostname] [--json]",
			Summary:     "Point your own hostnames at a VM",
			Description: "Manages the custom domains a VM answers on. A domain is routed, and gets a certificate, only once a DNS check has seen it resolve to this gateway; until then the claim is kept unverified together with the reason it failed, so the record can be fixed and re-checked rather than the hostname retyped. Verify and rm take either the hostname or the alias id that \"domain list --json\" prints.",
			// There is no bare form: a domain word on its own says nothing about
			// which VM or which claim is meant, so Run stays nil and the
			// dispatcher answers with the list of actions.
			Subcommands: []subSpec{
				{
					Name:  "list",
					Usage: "domain list <vm> [--json]",
					Desc:  "list a VM's custom domains and whether each is routed",
					Args:  []argSpec{{Name: "vm", Desc: "VM whose domains to list"}},
					JSON:  true,
					Run:   cmdDomainList,
				},
				{
					Name:  "add",
					Usage: "domain add <vm> <hostname>",
					Desc:  "claim a hostname for a VM and check it at once",
					Args: []argSpec{
						{Name: "vm", Desc: "VM the domain is for"},
						{Name: "hostname", Desc: "the domain to claim, for example app.example.org"},
					},
					Run: cmdDomainAdd,
				},
				{
					Name:  "verify",
					Usage: "domain verify <vm> <hostname|alias-id>",
					Desc:  "re-run the DNS check, after fixing the record",
					Args: []argSpec{
						{Name: "vm", Desc: "VM the domain belongs to"},
						{Name: "hostname", Desc: "the domain, or the alias id \"domain list --json\" prints"},
					},
					Run: cmdDomainVerify,
				},
				{
					Name:  "rm",
					Usage: "domain rm <vm> <hostname|alias-id>",
					Desc:  "release a hostname, freeing it to be claimed again",
					Args: []argSpec{
						{Name: "vm", Desc: "VM the domain belongs to"},
						{Name: "hostname", Desc: "the domain, or the alias id \"domain list --json\" prints"},
					},
					Run: cmdDomainRm,
				},
			},
			Examples: []string{
				"domain list dev",
				"domain list dev --json",
				"domain add dev app.example.org",
				"domain verify dev app.example.org",
				"domain rm dev app.example.org",
			},
		},
	}
}

func cmdDomainList(c *cmdCtx) error {
	m, container, err := c.domainTarget(c.arg(0))
	if err != nil {
		return err
	}
	list, err := m.List(container.ID)
	if err != nil {
		return err
	}

	if c.json {
		views := make([]domainJSON, 0, len(list))
		for _, alias := range list {
			views = append(views, newDomainJSON(alias))
		}
		return c.writeJSON(views)
	}

	if len(list) == 0 {
		c.printf("VM %q has no custom domains. Add one with \"domain add %s app.example.org\".\n", container.Name, container.Name)
		return nil
	}
	c.print("\n")
	c.printf("  %-40s %s\n", "DOMAIN", "STATE")
	c.printf("  %-40s %s\n", "------", "-----")
	for _, alias := range list {
		c.printf("  %-40s %s\n", alias.Hostname, domainState(alias))
	}
	c.print("\n")
	return nil
}

func cmdDomainAdd(c *cmdCtx) error {
	m, container, err := c.domainTarget(c.arg(0))
	if err != nil {
		return err
	}
	hostname := c.arg(1)
	if hostname == "" {
		return usagef("name the domain to add, as \"domain add %s app.example.org\"", container.Name)
	}

	// The manager's own errors already read as sentences — ErrInvalid carries
	// the reason the name is unusable, and a taken hostname says who holds it —
	// so they are passed on rather than restated.
	alias, err := m.Add(c.ctx, container, hostname)
	if err != nil {
		return domainFailure(err)
	}

	if alias.Verified {
		c.printf("%s now points at VM %q and is routed.\n", alias.Hostname, container.Name)
		return nil
	}
	// A failed DNS check is not a failed command: the claim is kept, because
	// what has to change is a record in the owner's zone, not the hostname they
	// typed here.
	c.printf("%s was added to VM %q but is not routed yet: %s\n", alias.Hostname, container.Name, domainReason(alias))
	c.print(domainAdvice(c.s.domain, container.Name, alias))
	return nil
}

func cmdDomainVerify(c *cmdCtx) error {
	m, container, err := c.domainTarget(c.arg(0))
	if err != nil {
		return err
	}
	alias, err := domainAlias(m, container, c.arg(1))
	if err != nil {
		return err
	}

	// Re-checking can demote as well as promote: a name that stopped pointing
	// here stops being routed the moment the check says so.
	alias, err = m.Recheck(c.ctx, container, alias.ID)
	if err != nil {
		return domainFailure(err)
	}
	if alias.Verified {
		c.printf("%s is verified and routed to VM %q.\n", alias.Hostname, container.Name)
		return nil
	}
	c.printf("%s is still not routed: %s\n", alias.Hostname, domainReason(alias))
	c.print(domainAdvice(c.s.domain, container.Name, alias))
	return nil
}

func cmdDomainRm(c *cmdCtx) error {
	m, container, err := c.domainTarget(c.arg(0))
	if err != nil {
		return err
	}
	alias, err := domainAlias(m, container, c.arg(1))
	if err != nil {
		return err
	}

	if err := m.Remove(c.ctx, container, alias.ID); err != nil {
		return domainFailure(err)
	}
	c.printf("%s was removed from VM %q; it is no longer routed there and the name can be claimed again.\n", alias.Hostname, container.Name)
	return nil
}

// domainTarget resolves the two things every action needs: the manager that
// performs the operation and the caller's own VM. The manager is checked first
// because it is nil on a deployment without a base domain, and an action must
// say so rather than dereference it.
func (c *cmdCtx) domainTarget(name string) (*aliases.Manager, *db.Container, error) {
	if c.s.aliases == nil {
		return nil, nil, errNoAliasManager
	}
	container, err := c.findContainer(name)
	if err != nil {
		return nil, nil, err
	}
	return c.s.aliases, container, nil
}

// domainAlias resolves the word an owner typed — a hostname or an alias id — to
// one of this VM's claims. Recheck and Remove take an id, but somebody reading
// "domain list" has the hostname in front of them and no id anywhere, so both
// have to work. Going through the VM's own list is also what keeps an id
// guessed from another VM from resolving at all.
func domainAlias(m *aliases.Manager, container *db.Container, ref string) (*db.ContainerAlias, error) {
	if ref == "" {
		return nil, usagef("name the domain, or the alias id \"domain list %s --json\" printed", container.Name)
	}
	list, err := m.List(container.ID)
	if err != nil {
		return nil, err
	}
	// Hostnames are stored lower-cased and without a trailing dot; an owner may
	// well type either.
	wanted := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(ref), "."))
	i := slices.IndexFunc(list, func(a *db.ContainerAlias) bool { return a.ID == ref || a.Hostname == wanted })
	if i < 0 {
		return nil, fmt.Errorf("VM %q has no custom domain %q; \"domain list %s\" shows the ones it has", container.Name, ref, container.Name)
	}
	return list[i], nil
}

// domainFailure translates the one manager error that would otherwise reach an
// owner as jargon. A claim can disappear between the lookup and the write when
// another VM verifies the same hostname, which deletes every competing claim.
func domainFailure(err error) error {
	if errors.Is(err, aliases.ErrNotFound) {
		return errors.New("that custom domain is no longer claimed by this VM")
	}
	return err
}

// domainState is the one-line answer for a claim: what it is, and since when it
// has been routed. VerifiedAt is cleared whenever an alias stops being
// verified, so a time here always belongs to the check currently in force.
func domainState(a *db.ContainerAlias) string {
	state := aliasState(a)
	if a.Verified && a.VerifiedAt != nil {
		state += " since " + a.VerifiedAt.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	return state
}

// domainReason is why a claim is not routed.
func domainReason(a *db.ContainerAlias) string {
	if a.LastError == "" {
		return "the DNS check has not run yet"
	}
	return a.LastError
}

// domainAdvice explains what has to happen before an unverified name is routed.
// A gateway with no verifier at all gets a different answer: nothing the owner
// does to their DNS can help there, so telling them to fix a record and
// re-check would send them after a problem that is not theirs.
func domainAdvice(domain, vm string, a *db.ContainerAlias) string {
	if a.LastError == aliases.ErrNoVerifier.Error() {
		return "This gateway cannot check DNS, so no custom domain can become routable on it. The claim is kept; ask the operator to configure domain verification.\n"
	}
	target := "this gateway"
	if domain != "" {
		target = domain
	}
	return fmt.Sprintf("Point %s at %s with a CNAME, then run \"domain verify %s %s\": routing and certificate issuance both wait for that check to pass.\n",
		a.Hostname, target, vm, a.Hostname)
}

// domainJSON is what "domain list --json" emits: one object per claim, carrying
// the id that verify and rm accept and the reason a name is not routed.
type domainJSON struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	Verified   bool   `json:"verified"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func newDomainJSON(a *db.ContainerAlias) domainJSON {
	v := domainJSON{
		ID:        a.ID,
		Hostname:  a.Hostname,
		Verified:  a.Verified,
		State:     aliasState(a),
		Error:     a.LastError,
		CreatedAt: formatTime(a.CreatedAt),
	}
	if a.VerifiedAt != nil {
		v.VerifiedAt = formatTime(*a.VerifiedAt)
	}
	return v
}

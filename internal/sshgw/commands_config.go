package sshgw

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/vmconfig"
)

func configCommands() []*command {
	return []*command{
		{
			Name:        "publish",
			Group:       groupConfig,
			Usage:       "publish <vm> [port] [public|private] [--json]",
			Summary:     "Show or set the port the VM serves on",
			Description: "Without a port it reports what the VM publishes today. A port changes which in-VM port the gateway proxies to; \"public\" serves it without a session and \"private\" requires one. Either word may be given on its own to change only the visibility. The agent's own port cannot be published.",
			Args: []argSpec{
				{Name: "vm", Desc: "VM to publish"},
				{Name: "port", Desc: "in-VM port the gateway proxies to", Optional: true},
				{Name: "visibility", Desc: "\"public\" or \"private\"", Optional: true},
			},
			JSON:     true,
			Examples: []string{"publish dev", "publish dev 8080", "publish dev 8080 public", "publish dev private", "publish dev --json"},
			Run:      cmdPublish,
		},
		{
			Name:        "nesting",
			Group:       groupConfig,
			Usage:       "nesting <vm> [on|off] [--json]",
			Summary:     "Show or set whether the VM may run containers",
			Description: "Nested containers are what make Docker, buildah and every image-based build work inside the VM. Without an argument it reports the state; \"on\" or \"off\" changes it. The setting only reaches the running instance at boot, so a running VM is told when it still owes a restart. A deployment-wide ban cannot be lifted from here.",
			Args: []argSpec{
				{Name: "vm", Desc: "VM to configure"},
				{Name: "state", Desc: "\"on\" or \"off\"", Optional: true},
			},
			JSON:     true,
			Examples: []string{"nesting dev", "nesting dev on", "nesting dev off"},
			Run:      cmdNesting,
		},
		{
			Name:        "task",
			Group:       groupConfig,
			Usage:       "task <vm> [--json]",
			Summary:     "Show or retry the VM's initial task",
			Description: "Reports the one-off task handed to the agent on first boot: its state, its text, the conversation it runs in and how many times the gateway picked it back up. A task still in progress is re-read from the VM first. The state is empty for a VM created without a task. A VM of yours actually named \"retry\" is addressed as \"task -- retry\", which stops the word being read as an action.",
			Args:        []argSpec{{Name: "vm", Desc: "VM whose task to inspect"}},
			JSON:        true,
			Subcommands: []subSpec{
				{
					Name:  "retry",
					Usage: "task retry <vm>",
					Desc:  "hand a failed task to the agent again",
					Args:  []argSpec{{Name: "vm", Desc: "VM whose failed task to hand over again"}},
					Run:   cmdTaskRetry,
				},
			},
			Examples: []string{"task dev", "task dev --json", "task retry dev"},
			Run:      cmdTask,
		},
		{
			Name:        "agent",
			Group:       groupConfig,
			Usage:       "agent <vm> [--json]",
			Summary:     "Show or install the PicoClaw agent build",
			Description: "Compares the agent running inside the VM with the build this gateway ships. Builds are identified by the checksum of the executable, because two integration revisions can carry the same PicoClaw version. \"agent update\" installs the gateway's build and restarts only the agent service. Both need the VM to be running. A VM of yours actually named \"update\" is addressed as \"agent -- update\".",
			Args:        []argSpec{{Name: "vm", Desc: "VM whose agent to inspect"}},
			JSON:        true,
			Subcommands: []subSpec{
				{
					Name:  "update",
					Usage: "agent update <vm>",
					Desc:  "install the gateway's agent build into the VM",
					Args:  []argSpec{{Name: "vm", Desc: "VM whose agent to update"}},
					Run:   cmdAgentUpdate,
				},
			},
			Examples: []string{"agent dev", "agent dev --json", "agent update dev"},
			Run:      cmdAgent,
		},
		{
			Name:        "share",
			Group:       groupConfig,
			Usage:       "share <vm> [--ttl=DURATION] [--json]",
			Summary:     "Create, list or revoke a shareable link",
			Description: "A share link opens the VM's workload without a session. It never reaches the agent, which can run commands inside the VM. Without --ttl the link does not expire. A VM of yours actually named \"list\" or \"revoke\" is addressed as \"share -- list\", which stops the word being read as an action.",
			Args:        []argSpec{{Name: "vm", Desc: "VM to share"}},
			Flags: []flagSpec{
				{Name: "ttl", Desc: "how long the new link lives, as a Go duration such as 24h", Value: true},
			},
			JSON: true,
			Subcommands: []subSpec{
				{
					Name:  "list",
					Usage: "share list <vm> [--json]",
					Desc:  "list the VM's share links",
					Args:  []argSpec{{Name: "vm", Desc: "VM whose links to list"}},
					JSON:  true,
					Run:   cmdShareList,
				},
				{
					Name:  "revoke",
					Usage: "share revoke <token>",
					Desc:  "delete one share link by its token",
					Args:  []argSpec{{Name: "token", Desc: "token of the link to delete"}},
					Run:   cmdShareRevoke,
				},
			},
			Examples: []string{"share dev", "share dev --ttl=24h", "share list dev --json", "share revoke abc123"},
			Run:      cmdShareCreate,
		},
		{
			Name:        "url",
			Group:       groupConfig,
			Usage:       "url <vm> [--json]",
			Summary:     "Print every address this VM answers on",
			Description: "Lists the VM's workload address, its agent interface, its page in the web dashboard, its browser shell and every verified custom domain pointed at it.",
			Args:        []argSpec{{Name: "vm", Desc: "VM to address"}},
			JSON:        true,
			Examples:    []string{"url dev", "url dev --json"},
			Run:         cmdURL,
		},
	}
}

// --- The view types these commands render under --json ---

// sharedLinkJSON describes one share link. The URL is empty in a deployment
// without a domain, where the token has nothing to be appended to.
type sharedLinkJSON struct {
	Token     string `json:"token"`
	URL       string `json:"url,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

// agentJSON mirrors picoclaw.AgentUpdate, renamed for the reader: the two
// "versions" it carries are checksums of the executable, not version strings.
type agentJSON struct {
	RunningBuild    string `json:"running_build"`
	PlatformBuild   string `json:"platform_build"`
	UpdateAvailable bool   `json:"update_available"`
}

// urlsJSON is every address one VM answers on. Each field is omitted when the
// deployment has no domain, because then no such address exists.
type urlsJSON struct {
	Workload  string   `json:"workload,omitempty"`
	Agent     string   `json:"agent,omitempty"`
	Dashboard string   `json:"dashboard,omitempty"`
	Shell     string   `json:"shell,omitempty"`
	Domains   []string `json:"domains,omitempty"`
}

// --- publish ---

func cmdPublish(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}

	port, public, changed, err := parsePublishArgs(c.args[1:], appPort(container), container.AppPublic)
	if err != nil {
		return err
	}
	if !changed {
		if c.json {
			return c.writeJSON(newVMJSON(container, c.s.domain))
		}
		c.printf("VM %q publishes port %d (%s).\n", container.Name, appPort(container), visibility(container.AppPublic))
		return nil
	}

	if !db.ValidAppPort(port) {
		return fmt.Errorf("port must be between 1 and 65535 and must not be the agent port %d", db.AgentPort)
	}
	if err := c.s.db.UpdateContainerPublish(container.ID, port, public); err != nil {
		return err
	}
	updated, err := c.s.db.GetContainerByID(container.ID)
	if err != nil {
		return err
	}
	// The agent is told which port to serve on; a stale answer would have it
	// configure software for a port that is no longer published. Not reaching the
	// VM must not fail the setting the owner just saved.
	if err := picoclaw.RefreshEnvironmentGuide(c.ctx, c.s.runtime, c.s.db, updated); err != nil {
		log.Printf("ssh publish %s: refresh agent guide: %v", updated.IncusName, err)
		c.printf("Warning: the agent was not told about the new port: %v\n", err)
	}

	if c.json {
		return c.writeJSON(newVMJSON(updated, c.s.domain))
	}
	c.printf("VM %q now publishes port %d (%s).\n", updated.Name, appPort(updated), visibility(updated.AppPublic))
	if url := vmURL(c.s.domain, updated.Name); url != "" {
		c.printf("  %s\n", url)
	}
	return nil
}

// parsePublishArgs reads the optional port and visibility words in either
// order, so that "publish dev private" can change only the visibility without
// making the owner restate a port they did not want to touch. changed reports
// whether anything at all was asked for.
func parsePublishArgs(args []string, port int, public bool) (int, bool, bool, error) {
	changed := false
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "public":
			public, changed = true, true
		case "private":
			public, changed = false, true
		default:
			value, err := strconv.Atoi(arg)
			if err != nil {
				return 0, false, false, usagef("%q is neither a port number nor \"public\" or \"private\"", arg)
			}
			port, changed = value, true
		}
	}
	return port, public, changed, nil
}

// --- nesting ---

func cmdNesting(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}

	if c.arg(1) == "" {
		// NestingEffective and NestingPending answer for nothing until the
		// deployment-wide ceiling is on the container.
		if err := c.s.db.AttachNestingPolicy(container); err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(newVMJSON(container, c.s.domain))
		}
		c.printf("Nested containers on %q: %s\n", container.Name, nestingSummary(container))
		return nil
	}

	wanted, err := parseOnOff(c.arg(1))
	if err != nil {
		return err
	}
	allowed, err := c.s.db.NestingAllowed()
	if err != nil {
		return fmt.Errorf("read the nesting policy: %w", err)
	}
	if wanted && !allowed {
		return fmt.Errorf("nested containers are disabled for this deployment; an administrator has to allow them first")
	}

	if err := c.s.db.UpdateContainerNesting(container.ID, wanted); err != nil {
		return err
	}
	updated, err := c.s.db.GetContainerByID(container.ID)
	if err != nil {
		return err
	}
	// Write it to the instance now so that a start from anywhere — this menu, the
	// dashboard, a host reboot — picks it up even if it never comes back through
	// here. Not reaching Incus must not lose the owner's choice: it is already
	// stored, and the next start re-applies it.
	if err := vmconfig.ApplyNesting(c.ctx, c.s.runtime, c.s.db, updated); err != nil {
		log.Printf("ssh nesting %s: apply to runtime: %v", updated.IncusName, err)
		c.printf("Warning: the setting is saved but did not reach the runtime: %v\n", err)
	}
	// The agent is told whether it can build with Docker; a stale answer sends it
	// down the "compile everything from source" path for no reason.
	if err := picoclaw.RefreshEnvironmentGuide(c.ctx, c.s.runtime, c.s.db, updated); err != nil {
		log.Printf("ssh nesting %s: refresh agent guide: %v", updated.IncusName, err)
	}
	if err := c.s.db.AttachNestingPolicy(updated); err != nil {
		return err
	}

	if c.json {
		return c.writeJSON(newVMJSON(updated, c.s.domain))
	}
	c.printf("Nested containers on %q: %s\n", updated.Name, nestingSummary(updated))
	if updated.NestingPending() {
		c.printf("  The running instance still has the old setting; \"restart %s\" applies it.\n", updated.Name)
	}
	return nil
}

// parseOnOff reads the word a switch-shaped setting is spelled with. It lives
// here because nesting was the first such setting; the administrator commands
// share it rather than growing a second spelling of the same answer.
func parseOnOff(word string) (bool, error) {
	switch strings.ToLower(word) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, usagef("say \"on\" or \"off\", not %q", word)
	}
}

// --- task ---

func cmdTask(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	// A task the agent is still working on is only as current as the last poll,
	// so it is read from the VM before it is reported. A VM that cannot answer
	// keeps its recorded state, which is why this only warns.
	if db.TaskInProgress(container.InitialTaskState) {
		if err := picoclaw.RefreshTaskState(c.ctx, c.s.runtime, c.s.db, container); err != nil {
			log.Printf("ssh task %s: refresh state: %v", container.IncusName, err)
			c.printf("Warning: the VM did not answer, so this is the last known state: %v\n", err)
		}
		if container, err = c.s.db.GetContainerByID(container.ID); err != nil {
			return err
		}
	}

	if c.json {
		return c.writeJSON(taskJSON{
			State:        container.InitialTaskState,
			Text:         container.InitialTask,
			Error:        container.InitialTaskError,
			Conversation: container.InitialTaskConversation,
			Resumes:      container.InitialTaskResumes,
		})
	}
	if container.InitialTaskState == "" {
		c.printf("VM %q was created without a task.\n", container.Name)
		return nil
	}
	c.printf("Task on %q: %s\n", container.Name, container.InitialTaskState)
	if container.InitialTask != "" {
		c.printf("  Text:         %s\n", container.InitialTask)
	}
	if container.InitialTaskError != "" {
		c.printf("  Error:        %s\n", container.InitialTaskError)
	}
	if container.InitialTaskConversation != "" {
		c.printf("  Conversation: %s\n", container.InitialTaskConversation)
	}
	c.printf("  Resumes:      %d\n", container.InitialTaskResumes)
	return nil
}

func cmdTaskRetry(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if err := c.s.db.RetryInitialTask(container.ID); err != nil {
		return err
	}
	// Delivery records its own outcome, so the VM is re-read afterwards to report
	// whether this attempt actually reached the agent.
	picoclaw.DeliverInitialTaskByID(c.ctx, c.s.runtime, c.s.db, container.ID, c.s.picoclawLLMCfg)
	updated, err := c.s.db.GetContainerByID(container.ID)
	if err != nil {
		return err
	}
	c.printf("Task on %q: %s\n", updated.Name, taskSummary(updated))
	return nil
}

// --- agent ---

func cmdAgent(c *cmdCtx) error {
	container, info, err := c.agentBuild()
	if err != nil {
		return err
	}

	if c.json {
		return c.writeJSON(agentJSON{
			RunningBuild:    info.CurrentVersion,
			PlatformBuild:   info.LatestVersion,
			UpdateAvailable: info.HasUpdate,
		})
	}
	c.printf("Agent on %q:\n", container.Name)
	c.printf("  Running build:  %s\n", info.CurrentVersion)
	c.printf("  Platform build: %s\n", info.LatestVersion)
	if info.HasUpdate {
		c.printf("  An update is available; \"agent update %s\" installs it.\n", container.Name)
	} else {
		c.print("  The VM is running the build this gateway ships.\n")
	}
	return nil
}

func cmdAgentUpdate(c *cmdCtx) error {
	container, info, err := c.agentBuild()
	if err != nil {
		return err
	}
	// Installing the build the VM already runs would restart the agent — and cut
	// whatever turn it is in the middle of — to change nothing.
	if !info.HasUpdate {
		c.printf("Agent on %q already runs the build this gateway ships (%s).\n", container.Name, info.LatestVersion)
		return nil
	}

	c.printf("Updating the agent on %q...\n", container.Name)
	if err := picoclaw.Update(c.ctx, c.s.runtime, container.IncusName); err != nil {
		return err
	}
	c.printf("Agent on %q updated from %s to %s.\n", container.Name, info.CurrentVersion, info.LatestVersion)
	return nil
}

// agentBuild resolves the VM named in the first argument and compares its
// running agent with the platform's. Both agent actions start here: the
// comparison is what one of them reports and what the other acts on.
func (c *cmdCtx) agentBuild() (*db.Container, *picoclaw.AgentUpdate, error) {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return nil, nil, err
	}
	// The checksum is read from the running process through systemd, which only
	// exists inside a VM that is up.
	if err := requireRunning(container); err != nil {
		return nil, nil, err
	}
	info, err := picoclaw.CheckUpdate(c.ctx, c.s.runtime, container.IncusName)
	if err != nil {
		return nil, nil, err
	}
	return container, info, nil
}

// --- share ---

func cmdShareCreate(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}

	var expiresAt *time.Time
	if raw := c.flag("ttl"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return usagef("--ttl must be a Go duration, such as 24h or 30m")
		}
		if ttl <= 0 {
			return fmt.Errorf("a link with a --ttl of %s would already have expired", raw)
		}
		deadline := time.Now().Add(ttl)
		expiresAt = &deadline
	}

	link, err := c.s.db.CreateSharedLink(container.ID, c.user.ID, expiresAt)
	if err != nil {
		return err
	}

	if c.json {
		return c.writeJSON(newSharedLinkJSON(link, container.Name, c.s.domain))
	}
	if url := shareURL(container.Name, c.s.domain, link.Token); url != "" {
		c.printf("%s\n", url)
	} else {
		// Without a domain there is no host to hang the token on, so the token
		// itself is all this deployment can hand over.
		c.printf("Share token: %s\n", link.Token)
		c.print("This deployment has no domain configured, so there is no address to share.\n")
	}
	if link.ExpiresAt != nil {
		c.printf("Expires: %s\n", formatExpiry(*link.ExpiresAt))
	}
	return nil
}

func cmdShareList(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	links, err := c.s.db.ListSharedLinksByContainer(container.ID)
	if err != nil {
		return err
	}

	if c.json {
		views := make([]sharedLinkJSON, 0, len(links))
		for _, link := range links {
			views = append(views, newSharedLinkJSON(link, container.Name, c.s.domain))
		}
		return c.writeJSON(views)
	}
	if len(links) == 0 {
		c.printf("VM %q has no share links.\n", container.Name)
		return nil
	}
	for _, link := range links {
		if url := shareURL(container.Name, c.s.domain, link.Token); url != "" {
			c.printf("  %s\n", url)
		} else {
			c.printf("  %s\n", link.Token)
		}
		if link.ExpiresAt != nil {
			c.printf("    expires %s\n", formatExpiry(*link.ExpiresAt))
		}
	}
	return nil
}

func cmdShareRevoke(c *cmdCtx) error {
	token := c.arg(0)
	if token == "" {
		return usagef("name the token to revoke; \"share list <vm>\" prints them")
	}

	// A token is the credential itself, so it is looked up globally — and then
	// the VM behind it has to be one of the caller's own. Anything else answers
	// the same way as an unknown token: a caller who does not own the share must
	// not be able to learn that it exists.
	link, err := c.s.db.LookupSharedLink(token)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoSuchShare
	}
	if err != nil {
		return err
	}
	container, err := c.s.db.GetContainerByID(link.ContainerID)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoSuchShare
	}
	if err != nil {
		return err
	}
	if container.OwnerID != c.user.ID {
		return errNoSuchShare
	}

	if err := c.s.db.DeleteSharedLinkByToken(token); err != nil {
		return err
	}
	c.printf("Share link on %q revoked.\n", container.Name)
	return nil
}

// errNoSuchShare is the single answer to a token this account cannot act on,
// whether it does not exist or belongs to somebody else.
var errNoSuchShare = errors.New("no such share link")

func newSharedLinkJSON(link *db.SharedLink, vmName, domain string) sharedLinkJSON {
	v := sharedLinkJSON{
		Token:     link.Token,
		URL:       shareURL(vmName, domain, link.Token),
		CreatedAt: formatTime(link.CreatedAt),
	}
	if link.ExpiresAt != nil {
		v.ExpiresAt = formatTime(*link.ExpiresAt)
	}
	return v
}

// shareURL points at the workload host, never at the agent: a share grants
// access to what the VM serves, and the agent can run commands inside it.
func shareURL(vmName, domain, token string) string {
	if domain == "" {
		return ""
	}
	return fmt.Sprintf("https://%s.%s/?share=%s", vmName, domain, token)
}

// --- url ---

func cmdURL(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if err := c.s.db.AttachAliases(container); err != nil {
		return err
	}

	v := urlsJSON{Workload: vmURL(c.s.domain, container.Name)}
	if c.s.domain != "" {
		v.Agent = "https://" + db.AgentHostPrefix + container.Name + "." + c.s.domain
		v.Dashboard = "https://" + c.s.domain + "/dashboard/vms"
		v.Shell = "https://" + c.s.domain + "/dashboard/vms/" + container.ID + "/shell"
	}
	// Only a verified alias is routed here, so an unverified one is not yet an
	// address this VM answers on.
	for _, alias := range container.Aliases {
		if alias.Verified {
			v.Domains = append(v.Domains, "https://"+alias.Hostname)
		}
	}

	if c.json {
		return c.writeJSON(v)
	}
	if c.s.domain == "" {
		c.print("This deployment has no domain configured, so its VMs have no addresses.\n")
		return nil
	}
	c.printf("  Workload:   %s\n", v.Workload)
	c.printf("  Agent:      %s\n", v.Agent)
	c.printf("  Dashboard:  %s\n", v.Dashboard)
	c.printf("  Web shell:  %s\n", v.Shell)
	for _, domain := range v.Domains {
		c.printf("  Domain:     %s\n", domain)
	}
	return nil
}

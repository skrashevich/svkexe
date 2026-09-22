package sshgw

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/vmconfig"
)

func vmCommands() []*command {
	return []*command{
		{
			Name:        "ls",
			Aliases:     []string{"ps"},
			Group:       groupVM,
			Usage:       "ls [--json]",
			Summary:     "List your VMs",
			Description: "Lists every VM you own with its status and resources.",
			JSON:        true,
			Examples:    []string{"ls", "ls --json"},
			Run:         cmdLs,
		},
		{
			Name:        "new",
			Group:       groupVM,
			Usage:       "new <name> [--cpu=N] [--memory=MB] [--disk=GB] [--task=\"...\"]",
			Summary:     "Create and start a VM",
			Description: "Creates a VM from the platform base image, starts it and installs the agent. Without the resource flags it gets 2 cores, 2048 MB of RAM and 10 GB of disk. A task given here is handed to the agent once the VM is up.",
			Args:        []argSpec{{Name: "name", Desc: "VM name: 2-63 lowercase letters, digits and hyphens; it becomes the VM's hostname"}},
			Flags: []flagSpec{
				{Name: "cpu", Desc: "CPU cores", Value: true},
				{Name: "memory", Desc: "memory in MB", Value: true},
				{Name: "disk", Desc: "disk in GB", Value: true},
				{Name: "task", Desc: "task handed to the agent on first boot", Value: true},
			},
			Examples: []string{"new dev", "new dev --cpu=4 --memory=4096 --task=\"set up a Go project\""},
			Run:      cmdNew,
		},
		{
			Name:        "rm",
			Group:       groupVM,
			Usage:       "rm <name> [--force]",
			Summary:     "Delete a VM",
			Description: "Stops the VM if it is running and deletes it along with its disk. This cannot be undone. A one-shot invocation (ssh gateway \"rm dev\") has to add --force, because the same command line means something else entirely while a VM of that name is your SSH login.",
			Args:        []argSpec{{Name: "name", Desc: "VM to delete"}},
			Flags:       []flagSpec{{Name: "force", Desc: "required to delete from a one-shot invocation"}},
			Examples:    []string{"rm dev", "rm dev --force"},
			Run:         cmdRm,
		},
		{
			Name:        "start",
			Group:       groupVM,
			Usage:       "start <name>",
			Summary:     "Start a VM",
			Description: "Boots the VM, applies its current nesting setting and re-installs the agent configuration.",
			Args:        []argSpec{{Name: "name", Desc: "VM to start"}},
			Examples:    []string{"start dev"},
			Run:         cmdStart,
		},
		{
			Name:        "stop",
			Group:       groupVM,
			Usage:       "stop <name>",
			Summary:     "Stop a VM",
			Description: "Shuts the VM down. Its disk and data are kept.",
			Args:        []argSpec{{Name: "name", Desc: "VM to stop"}},
			Examples:    []string{"stop dev"},
			Run:         cmdStop,
		},
		{
			Name:        "restart",
			Group:       groupVM,
			Usage:       "restart <name>",
			Summary:     "Restart a VM",
			Description: "Stops and starts the VM. This is how a setting that only takes effect at boot, such as nesting, is applied.",
			Args:        []argSpec{{Name: "name", Desc: "VM to restart"}},
			Examples:    []string{"restart dev"},
			Run:         cmdRestart,
		},
		{
			Name:        "rename",
			Group:       groupVM,
			Usage:       "rename <old> <new>",
			Summary:     "Rename a VM",
			Description: "Changes the VM's display name. Its address and its agent are unaffected.",
			Args: []argSpec{
				{Name: "old", Desc: "current name"},
				{Name: "new", Desc: "new name"},
			},
			Examples: []string{"rename dev staging"},
			Run:      cmdRename,
		},
		{
			Name:        "stat",
			Group:       groupVM,
			Usage:       "stat <name> [--json]",
			Summary:     "Show everything known about a VM",
			Description: "Reports status, resources, address, published port, nesting, custom domains and the state of the initial task.",
			Args:        []argSpec{{Name: "name", Desc: "VM to describe"}},
			JSON:        true,
			Examples:    []string{"stat dev", "stat dev --json"},
			Run:         cmdStat,
		},
		{
			Name:        "ssh",
			Group:       groupVM,
			Usage:       "ssh <name>",
			Summary:     "Open a shell inside a VM",
			Description: "Attaches an interactive root shell to the VM. Connecting with the VM's name as the SSH login does the same thing in one step: ssh dev@<gateway>.",
			Args:        []argSpec{{Name: "name", Desc: "VM to attach to"}},
			Examples:    []string{"ssh dev"},
			Run:         cmdSSH,
		},
		{
			Name:        "recreate",
			Group:       groupVM,
			Usage:       "recreate <name> [--force]",
			Summary:     "Rebuild a VM from the latest image, keeping /data",
			Description: "Backs up /data, deletes the container, builds a new one from the current base image and restores /data. Everything outside /data is lost, so a one-shot invocation has to add --force.",
			Args:        []argSpec{{Name: "name", Desc: "VM to rebuild"}},
			Flags:       []flagSpec{{Name: "force", Desc: "required to rebuild from a one-shot invocation"}},
			Examples:    []string{"recreate dev", "recreate dev --force"},
			Run:         cmdRecreate,
		},
	}
}

func cmdLs(c *cmdCtx) error {
	containers, err := c.s.db.ListAccessibleContainers(c.user.ID)
	if err != nil {
		return err
	}
	if c.json {
		views := make([]vmJSON, 0, len(containers))
		for _, container := range containers {
			views = append(views, newVMJSON(container, c.s.domain))
		}
		return c.writeJSON(views)
	}

	if len(containers) == 0 {
		c.print("No VMs found. Create one with \"new <name>\".\n")
		return nil
	}
	c.print("\n")
	c.printf("  %-20s %-10s %-6s %-8s %-6s\n", "NAME", "STATUS", "CPU", "MEMORY", "DISK")
	c.printf("  %-20s %-10s %-6s %-8s %-6s\n", "----", "------", "---", "------", "----")
	for _, container := range containers {
		c.printf("  %-20s %s %-7s %-6d %-8s %-6s\n",
			container.Name,
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

func cmdNew(c *cmdCtx) error {
	name := c.arg(0)
	if name == "" {
		return usagef("name the VM to create")
	}
	if err := checkVMName(name); err != nil {
		return err
	}
	if _, err := c.s.db.GetContainerByName(name, c.user.ID); err == nil {
		return fmt.Errorf("VM %q already exists", name)
	}

	cpu, err := c.intFlag("cpu", 2, maxCPU)
	if err != nil {
		return err
	}
	memory, err := c.intFlag("memory", 2048, maxMemoryMB)
	if err != nil {
		return err
	}
	disk, err := c.intFlag("disk", 10, maxDiskGB)
	if err != nil {
		return err
	}
	task := strings.TrimSpace(c.flag("task"))
	if len(task) > db.MaxInitialTaskLen {
		return fmt.Errorf("the task is longer than the %d characters the agent accepts", db.MaxInitialTaskLen)
	}

	c.printf("Creating VM %q...\n", name)

	// The owner's per-VM nesting wish is set afterwards from "nesting", so a VM
	// born here takes the platform default.
	nestingAllowed, err := c.s.db.NestingAllowed()
	if err != nil {
		return fmt.Errorf("read the nesting policy: %w", err)
	}

	rtContainer, err := c.s.runtime.Create(c.ctx, runtime.CreateOpts{
		Name:     name,
		OwnerID:  c.user.ID,
		Image:    picoclaw.DefaultImage,
		CPULimit: cpu,
		MemoryMB: memory,
		DiskGB:   disk,
		Nesting:  nestingAllowed,
	})
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}

	dbContainer := &db.Container{
		ID:          uuid.New().String(),
		Name:        name,
		OwnerID:     c.user.ID,
		IncusName:   rtContainer.Name,
		Status:      rtContainer.Status,
		IPAddress:   rtContainer.IP,
		CPULimit:    cpu,
		MemoryMB:    memory,
		DiskGB:      disk,
		InitialTask: task,
		Nesting:     db.DefaultNesting,
	}
	if task != "" {
		dbContainer.InitialTaskState = db.TaskPending
	}
	if err := c.s.db.CreateContainer(dbContainer); err != nil {
		return fmt.Errorf("save VM: %w", err)
	}
	if err := vmconfig.MarkStarted(c.s.db, dbContainer, nestingAllowed); err != nil {
		c.printf("Warning: could not record the nesting setting: %v\n", err)
	}

	// Incus hands back a stopped instance, so bring it up as part of creation —
	// a new VM is expected to be usable without a separate "start" command.
	if !strings.EqualFold(dbContainer.Status, "running") {
		c.printf("Starting VM %q...\n", name)
		if err := c.s.runtime.Start(c.ctx, dbContainer.IncusName); err != nil {
			_ = c.s.db.UpdateContainerStatus(dbContainer.ID, "stopped", dbContainer.IPAddress)
			return fmt.Errorf("start VM: %w", err)
		}
	}
	if rtc, err := c.s.runtime.Get(c.ctx, dbContainer.IncusName); err == nil && rtc != nil {
		dbContainer.IPAddress = rtc.IP
	}

	if c.s.materializer != nil {
		if err := picoclaw.SetupContainer(c.ctx, c.s.runtime, c.s.db, c.s.materializer, dbContainer, c.s.picoclawLLMCfg); err != nil {
			_ = c.s.db.UpdateContainerStatus(dbContainer.ID, "error", dbContainer.IPAddress)
			return fmt.Errorf("PicoClaw setup: %w", err)
		}
		picoclaw.DeliverInitialTaskByID(c.ctx, c.s.runtime, c.s.db, dbContainer.ID, c.s.picoclawLLMCfg)
	}

	_ = c.s.db.UpdateContainerStatus(dbContainer.ID, "running", dbContainer.IPAddress)
	c.printf("VM %q created and running.\n", name)
	if url := vmURL(c.s.domain, name); url != "" {
		c.printf("  %s\n", url)
	}
	return nil
}

func cmdRm(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if err := c.confirmDestructive(fmt.Sprintf("deleting VM %q", container.Name)); err != nil {
		return err
	}

	if container.Status == "running" {
		c.printf("Stopping VM %q...\n", container.Name)
		if err := c.s.runtime.Stop(c.ctx, container.IncusName); err != nil {
			return fmt.Errorf("stop VM: %w", err)
		}
	}

	c.printf("Deleting VM %q...\n", container.Name)
	if err := c.s.runtime.Delete(c.ctx, container.IncusName); err != nil {
		return fmt.Errorf("delete VM: %w", err)
	}
	if err := c.s.db.DeleteContainer(container.ID); err != nil {
		return fmt.Errorf("remove VM record: %w", err)
	}

	c.printf("VM %q deleted.\n", container.Name)
	return nil
}

func cmdStart(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if container.Status == "running" {
		c.printf("VM %q is already running.\n", container.Name)
		return nil
	}

	c.printf("Starting VM %q...\n", container.Name)
	// The runtime reads the nesting setting at boot, so a start is the only
	// place a change made elsewhere can take effect.
	if err := vmconfig.PrepareStart(c.ctx, c.s.runtime, c.s.db, container); err != nil {
		log.Printf("ssh start %s: apply nesting: %v", container.IncusName, err)
	}
	if err := c.s.runtime.Start(c.ctx, container.IncusName); err != nil {
		return err
	}

	// Re-apply PicoClaw config on every start.
	if c.s.materializer != nil {
		if err := picoclaw.SetupContainer(c.ctx, c.s.runtime, c.s.db, c.s.materializer, container, c.s.picoclawLLMCfg); err != nil {
			_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
			return fmt.Errorf("PicoClaw setup: %w", err)
		}
		picoclaw.DeliverInitialTaskByID(c.ctx, c.s.runtime, c.s.db, container.ID, c.s.picoclawLLMCfg)
	}

	_ = c.s.db.UpdateContainerStatus(container.ID, "running", container.IPAddress)
	c.printf("VM %q started.\n", container.Name)
	return nil
}

func cmdStop(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if container.Status == "stopped" {
		c.printf("VM %q is already stopped.\n", container.Name)
		return nil
	}

	c.printf("Stopping VM %q...\n", container.Name)
	if err := c.s.runtime.Stop(c.ctx, container.IncusName); err != nil {
		return err
	}
	_ = c.s.db.UpdateContainerStatus(container.ID, "stopped", container.IPAddress)
	c.printf("VM %q stopped.\n", container.Name)
	return nil
}

func cmdRestart(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}

	if container.Status == "running" {
		c.printf("Stopping VM %q...\n", container.Name)
		if err := c.s.runtime.Stop(c.ctx, container.IncusName); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}

	c.printf("Starting VM %q...\n", container.Name)
	if err := vmconfig.PrepareStart(c.ctx, c.s.runtime, c.s.db, container); err != nil {
		log.Printf("ssh restart %s: apply nesting: %v", container.IncusName, err)
	}
	if err := c.s.runtime.Start(c.ctx, container.IncusName); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Re-apply PicoClaw config on every start.
	if c.s.materializer != nil {
		if err := picoclaw.SetupContainer(c.ctx, c.s.runtime, c.s.db, c.s.materializer, container, c.s.picoclawLLMCfg); err != nil {
			_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
			return fmt.Errorf("PicoClaw setup: %w", err)
		}
		picoclaw.DeliverInitialTaskByID(c.ctx, c.s.runtime, c.s.db, container.ID, c.s.picoclawLLMCfg)
	}

	_ = c.s.db.UpdateContainerStatus(container.ID, "running", container.IPAddress)
	c.printf("VM %q restarted.\n", container.Name)
	return nil
}

func cmdRename(c *cmdCtx) error {
	if len(c.args) < 2 {
		return usagef("give the current name and the new one")
	}
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}

	newName := c.arg(1)
	if err := checkVMName(newName); err != nil {
		return err
	}
	if _, err := c.s.db.GetContainerByName(newName, c.user.ID); err == nil {
		return fmt.Errorf("VM %q already exists", newName)
	}

	if err := c.s.db.RenameContainer(container.ID, newName); err != nil {
		return err
	}
	c.printf("VM %q renamed to %q.\n", container.Name, newName)
	return nil
}

func cmdStat(c *cmdCtx) error {
	container, err := c.findAccessibleContainer(c.arg(0))
	if err != nil {
		return err
	}
	if err := c.s.db.AttachNestingPolicy(container); err != nil {
		return err
	}
	if err := c.s.db.AttachAliases(container); err != nil {
		return err
	}

	if c.json {
		return c.writeJSON(newVMJSON(container, c.s.domain))
	}

	c.print("\n")
	c.printf("  Name:       %s\n", container.Name)
	c.printf("  Status:     %s %s\n", statusIcon(container.Status), container.Status)
	c.printf("  CPU:        %d cores\n", container.CPULimit)
	c.printf("  Memory:     %s\n", formatMB(container.MemoryMB))
	c.printf("  Disk:       %s\n", formatGB(container.DiskGB))
	if container.IPAddress != "" {
		c.printf("  IP:         %s\n", container.IPAddress)
	}
	if url := vmURL(c.s.domain, container.Name); url != "" {
		c.printf("  URL:        %s\n", url)
	}
	c.printf("  Port:       %d (%s)\n", appPort(container), visibility(container.AppPublic))
	c.printf("  Nesting:    %s\n", nestingSummary(container))
	for _, alias := range container.Aliases {
		c.printf("  Domain:     %s (%s)\n", alias.Hostname, aliasState(alias))
	}
	if container.InitialTaskState != "" {
		c.printf("  Task:       %s\n", taskSummary(container))
	}
	c.printf("  Created:    %s\n", container.CreatedAt.Format("2006-01-02 15:04"))
	c.print("\n")
	return nil
}

func cmdSSH(c *cmdCtx) error {
	container, err := c.findAccessibleContainer(c.arg(0))
	if err != nil {
		return err
	}
	if err := requireRunning(container); err != nil {
		return err
	}

	c.printf("Connecting to %s...\n", container.Name)
	if err := c.s.attach(c.ctx, c.sess, container, nil); err != nil {
		return err
	}
	c.print("\nSession ended.\n")
	return nil
}

func cmdRecreate(c *cmdCtx) error {
	container, err := c.findContainer(c.arg(0))
	if err != nil {
		return err
	}
	if container.Status == "creating" || container.Status == "recreating" {
		return fmt.Errorf("VM %q is busy (status: %s); wait for it to settle", container.Name, container.Status)
	}
	if err := c.confirmDestructive(fmt.Sprintf("rebuilding VM %q loses everything outside /data", container.Name)); err != nil {
		return err
	}

	_ = c.s.db.UpdateContainerStatus(container.ID, "recreating", container.IPAddress)

	// Booting the old instance for the backup is a real start, running for as
	// long as the tar takes, so it honours the current setting like any other.
	if err := vmconfig.PrepareStart(c.ctx, c.s.runtime, c.s.db, container); err != nil {
		log.Printf("ssh recreate: apply nesting before backup for %s: %v", container.IncusName, err)
	}
	if err := c.s.runtime.Start(c.ctx, container.IncusName); err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
		return fmt.Errorf("start VM for backup: %w", err)
	}
	c.printf("Backing up /data from %q...\n", container.Name)
	backupData, err := picoclaw.BackupData(c.ctx, c.s.runtime, container.IncusName)
	if err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
		return fmt.Errorf("back up VM: %w", err)
	}
	if err := c.s.runtime.Stop(c.ctx, container.IncusName); err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
		return fmt.Errorf("stop VM: %w", err)
	}

	c.print("Deleting old container...\n")
	if err := c.s.runtime.Delete(c.ctx, container.IncusName); err != nil {
		// The status has to leave "recreating" even here: recreate refuses to
		// run against a VM in that state, so a failure that left it there would
		// lock the VM out of the very command that could rebuild it.
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", container.IPAddress)
		return fmt.Errorf("delete VM: %w", err)
	}

	// Create new container from fresh image. The rebuild resolves nesting afresh
	// so the new instance lands on the setting that is current now.
	c.printf("Creating new container from %s image...\n", picoclaw.DefaultImage)
	effectiveNesting, err := vmconfig.EffectiveNesting(c.s.db, container)
	if err != nil {
		log.Printf("ssh recreate %s: resolve nesting: %v", container.IncusName, err)
	}
	_, err = c.s.runtime.Create(c.ctx, runtime.CreateOpts{
		Name:     container.Name,
		OwnerID:  c.user.ID,
		Image:    picoclaw.DefaultImage,
		CPULimit: container.CPULimit,
		MemoryMB: container.MemoryMB,
		DiskGB:   container.DiskGB,
		Nesting:  effectiveNesting,
	})
	if err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", "")
		return fmt.Errorf("create VM: %w", err)
	}
	if err := vmconfig.MarkStarted(c.s.db, container, effectiveNesting); err != nil {
		log.Printf("ssh recreate %s: record nesting: %v", container.IncusName, err)
	}

	c.printf("Starting %q...\n", container.Name)
	if err := c.s.runtime.Start(c.ctx, container.IncusName); err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "stopped", "")
		return fmt.Errorf("start VM: %w", err)
	}

	ip := ""
	if rtc, err := c.s.runtime.Get(c.ctx, container.IncusName); err == nil {
		ip = rtc.IP
	}

	c.print("Restoring /data...\n")
	if err := picoclaw.RestoreData(c.ctx, c.s.runtime, container.IncusName, backupData); err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", ip)
		return fmt.Errorf("restore data: %w", err)
	}
	c.print("Setting up PicoClaw...\n")
	if err := picoclaw.SetupContainer(c.ctx, c.s.runtime, c.s.db, c.s.materializer, container, c.s.picoclawLLMCfg); err != nil {
		_ = c.s.db.UpdateContainerStatus(container.ID, "error", ip)
		return fmt.Errorf("PicoClaw setup: %w", err)
	}

	_ = c.s.db.UpdateContainerStatus(container.ID, "running", ip)
	c.printf("VM %q recreated successfully.\n", container.Name)
	return nil
}

// Guard rails on what a single VM may ask for. They are not a quota — the
// platform has none — but a typo in a scripted "new" would otherwise reserve
// the whole host, and an agent driving this shell writes those numbers without
// a human reading them back.
const (
	maxCPU      = 64
	maxMemoryMB = 131072
	maxDiskGB   = 1024
)

// intFlag reads a numeric flag, falling back to the platform default. It parses
// strictly: fmt.Sscanf would read "4abc" as 4 and report no error, which is how
// a mistyped size becomes a VM nobody asked for.
func (c *cmdCtx) intFlag(name string, fallback, max int) (int, error) {
	raw := c.flag(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, usagef("--%s must be a positive whole number", name)
	}
	if value > max {
		return 0, usagef("--%s is capped at %d on this gateway", name, max)
	}
	return value, nil
}

// checkVMName applies the platform-wide rule rather than a local one. A name is
// a DNS label in every address the VM answers on, and ReservedName additionally
// keeps it from shadowing a routing prefix: a VM called "agent-foo" would take
// the traffic meant for the agent of the VM called "foo".
func checkVMName(name string) error {
	if !db.ValidContainerName(name) {
		return fmt.Errorf("invalid VM name %q: use 2-63 lowercase letters, digits and hyphens, not starting with %q or with a port prefix such as \"3000-\"", name, db.AgentHostPrefix)
	}
	return nil
}

// confirmDestructive refuses a destructive command that arrived as a one-shot
// invocation without saying so explicitly.
//
// The login decides what "ssh dev@gateway rm foo" means: inside the VM while
// the VM exists, and a management command — deleting the VM named foo — once it
// does not. A script or an agent pinned to the old name would otherwise cross
// that line silently, so the management side asks to be named.
func (c *cmdCtx) confirmDestructive(what string) error {
	if c.interactive || c.hasFlag("force") {
		return nil
	}
	return usagef("%s is destructive; in a one-shot invocation say so with --force", what)
}

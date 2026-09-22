package sshgw

import (
	"fmt"
	"strings"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/version"
)

// usageColumn is where the summaries line up in the command list. It leaves
// room for a summary on an 80-column terminal.
const usageColumn = 46

func sessionCommands() []*command {
	return []*command{
		{
			Name:        "help",
			Group:       groupSession,
			Usage:       "help [command] [action] [--json]",
			Summary:     "List the commands available to you",
			Description: "Without arguments it lists every command you may run, grouped by area. With a command name it explains that command's arguments and flags. With --json it emits the whole catalogue as one JSON document, which is the form meant for programs and LLM agents.",
			Args: []argSpec{
				{Name: "command", Desc: "command to explain", Optional: true},
				{Name: "action", Desc: "one action of that command, as in \"help share revoke\"", Optional: true},
			},
			JSON:     true,
			Examples: []string{"help", "help new", "help share revoke", "help --json"},
			Run:      cmdHelp,
		},
		{
			Name:        "exit",
			Group:       groupSession,
			Usage:       "exit",
			Summary:     "Close the session",
			Description: "Leaves the management menu and closes the SSH connection.",
			Aliases:     []string{"quit"},
			Interactive: true,
			Run: func(c *cmdCtx) error {
				c.print("Goodbye.\n")
				return errExitSession
			},
		},
	}
}

func cmdHelp(c *cmdCtx) error {
	if c.json {
		return c.writeJSON(newHelpDoc(c.s, c.user))
	}
	if name := c.arg(0); name != "" {
		return c.helpForCommand(name, c.arg(1))
	}

	c.print("\nsvkexe management shell. Commands available to you:\n\n")
	group := ""
	for _, cmd := range visibleCommands(c.user) {
		if cmd.Group != group {
			group = cmd.Group
			c.printf("  %s\n", strings.ToUpper(group))
		}
		// A usage line wider than the column takes a line of its own rather
		// than pushing its summary off the right of an 80-column terminal.
		if len(cmd.Usage) > usageColumn {
			c.printf("    %s\n    %*s %s\n", cmd.Usage, usageColumn, "", cmd.Summary)
			continue
		}
		c.printf("    %-*s %s\n", usageColumn, cmd.Usage, cmd.Summary)
	}
	c.print("\n  \"help <command>\" explains one command and \"help <command> <action>\" one of\n")
	c.print("  its actions; \"help --json\" prints the whole catalogue as JSON for scripts\n")
	c.print("  and LLM agents.\n\n")
	return nil
}

func (c *cmdCtx) helpForCommand(name, subName string) error {
	cmd, ok := byName[name]
	if !ok || (cmd.AdminOnly && !isAdmin(c.user)) || (c.user.Role == db.GuestRole && !guestCommandAllowed(cmd.Name)) {
		return fmt.Errorf("no such command %q", name)
	}

	// "help share revoke" explains that one action rather than the whole group.
	if subName != "" {
		sub := cmd.sub(subName)
		if sub == nil {
			return fmt.Errorf("%q has no action %q: %s", cmd.Name, subName, subcommandNames(cmd))
		}
		c.printf("\n  %s\n", sub.Usage)
		c.printf("  %s\n\n", sub.Desc)
		c.printArgs(sub.Args)
		c.printFlags(withJSONFlag(sub.Flags, sub.JSON))
		return nil
	}

	c.printf("\n  %s\n", cmd.Usage)
	c.printf("  %s\n\n", cmd.Description)
	if len(cmd.Subcommands) > 0 {
		c.print("  Actions:\n")
		for _, sub := range cmd.Subcommands {
			c.printf("    %-42s %s\n", sub.Usage, sub.Desc)
		}
		c.printf("\n    \"help %s <action>\" explains one of them.\n\n", cmd.Name)
	}
	c.printArgs(cmd.Args)
	c.printFlags(cmd.allFlags())
	if len(cmd.Examples) > 0 {
		c.print("  Examples:\n")
		for _, e := range cmd.Examples {
			c.printf("    %s\n", e)
		}
		c.print("\n")
	}
	return nil
}

func (c *cmdCtx) printArgs(args []argSpec) {
	if len(args) == 0 {
		return
	}
	c.print("  Arguments:\n")
	for _, a := range args {
		c.printf("    %-14s %s%s\n", a.Name, a.Desc, optionalSuffix(a.Optional))
	}
	c.print("\n")
}

func (c *cmdCtx) printFlags(flags []flagSpec) {
	if len(flags) == 0 {
		return
	}
	c.print("  Flags:\n")
	for _, f := range flags {
		name := "--" + f.Name
		if f.Value {
			name += "=..."
		}
		c.printf("    %-14s %s\n", name, f.Desc)
	}
	c.print("\n")
}

func optionalSuffix(optional bool) string {
	if optional {
		return " (optional)"
	}
	return ""
}

// allFlags is the bare form's flags plus the --json it accepts, if it does. An
// action's own flags are read from its subSpec, so help and the dispatcher
// never disagree about what a given action accepts.
func (cmd *command) allFlags() []flagSpec {
	return withJSONFlag(cmd.Flags, cmd.JSON)
}

// --- The machine-readable catalogue ---

// helpDoc is what "help --json" prints: enough for a program that has never
// seen this gateway to work out what it can do and how to ask for it.
type helpDoc struct {
	Gateway     string        `json:"gateway"`
	Description string        `json:"description"`
	Version     string        `json:"version"`
	User        helpUser      `json:"user"`
	Invocation  helpUsage     `json:"invocation"`
	Commands    []helpCommand `json:"commands"`
}

type helpUser struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Admin bool   `json:"admin"`
}

// helpUsage explains the three ways the gateway can be driven, because the
// command list alone does not tell a caller that it need not open a prompt.
type helpUsage struct {
	Interactive string `json:"interactive"`
	OneShot     string `json:"one_shot"`
	DirectVM    string `json:"direct_vm"`
	JSONOutput  string `json:"json_output"`
	ExitCodes   string `json:"exit_codes"`
}

type helpCommand struct {
	Name        string     `json:"name"`
	Group       string     `json:"group"`
	Usage       string     `json:"usage"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Subcommands []helpSub  `json:"subcommands,omitempty"`
	Args        []helpArg  `json:"args,omitempty"`
	Flags       []helpFlag `json:"flags,omitempty"`
	Examples    []string   `json:"examples,omitempty"`
	AdminOnly   bool       `json:"admin_only"`
	JSON        bool       `json:"supports_json"`
}

// helpSub describes one action in full. Its arguments and flags are its own:
// an agent reading this must not have to guess which of a group's flags apply
// to the action it is about to run.
type helpSub struct {
	Name  string     `json:"name"`
	Usage string     `json:"usage"`
	Desc  string     `json:"description"`
	Args  []helpArg  `json:"args,omitempty"`
	Flags []helpFlag `json:"flags,omitempty"`
	JSON  bool       `json:"supports_json"`
}

type helpArg struct {
	Name     string `json:"name"`
	Desc     string `json:"description"`
	Optional bool   `json:"optional"`
	Repeated bool   `json:"repeated,omitempty"`
}

type helpFlag struct {
	Name  string `json:"name"`
	Desc  string `json:"description"`
	Value bool   `json:"takes_value"`
}

func newHelpDoc(s *Server, user *db.User) helpDoc {
	host := s.domain
	if host == "" {
		host = "this gateway"
	}
	doc := helpDoc{
		Gateway:     host,
		Description: "svkexe manages persistent Linux VMs, each running a PicoClaw coding agent. This SSH endpoint is the same management surface as the web dashboard.",
		Version:     version.Get().Short(),
		User:        helpUser{Email: user.Email, Role: user.Role, Admin: isAdmin(user)},
		Invocation: helpUsage{
			Interactive: fmt.Sprintf("ssh %s — opens this menu; any login name works, the SSH key identifies you. ssh %s@%s always opens it, even when a VM of yours has the same name as your login", host, menuLogin, host),
			OneShot:     fmt.Sprintf("ssh %s \"<command>\" — runs one command and exits", host),
			DirectVM:    fmt.Sprintf("ssh <vm>@%s — opens a shell in that VM, or runs a command inside it", host),
			JSONOutput:  "commands marked supports_json accept --json and then print one JSON document per invocation",
			ExitCodes:   fmt.Sprintf("%d on success, %d when the command failed, %d when the command does not exist", 0, exitFailure, exitUnknownCommand),
		},
	}
	for _, cmd := range visibleCommands(user) {
		hc := helpCommand{
			Name:        cmd.Name,
			Group:       cmd.Group,
			Usage:       cmd.Usage,
			Summary:     cmd.Summary,
			Description: cmd.Description,
			Examples:    cmd.Examples,
			AdminOnly:   cmd.AdminOnly,
			JSON:        cmd.JSON,
		}
		for _, sub := range cmd.Subcommands {
			hc.Subcommands = append(hc.Subcommands, helpSub{
				Name:  sub.Name,
				Usage: sub.Usage,
				Desc:  sub.Desc,
				Args:  helpArgs(sub.Args),
				Flags: helpFlags(withJSONFlag(sub.Flags, sub.JSON)),
				JSON:  sub.JSON,
			})
		}
		hc.Args = helpArgs(cmd.Args)
		hc.Flags = helpFlags(cmd.allFlags())
		doc.Commands = append(doc.Commands, hc)
	}
	return doc
}

func helpArgs(args []argSpec) []helpArg {
	var out []helpArg
	for _, a := range args {
		out = append(out, helpArg{Name: a.Name, Desc: a.Desc, Optional: a.Optional, Repeated: a.Repeated})
	}
	return out
}

func helpFlags(flags []flagSpec) []helpFlag {
	var out []helpFlag
	for _, f := range flags {
		out = append(out, helpFlag{Name: f.Name, Desc: f.Desc, Value: f.Value})
	}
	return out
}

// agentBrief greets a session that never asked for a terminal. That is how a
// script or an LLM agent arrives, and what it needs first is not a banner but
// the one command that describes everything else.
func agentBrief(s *Server, user *db.User) string {
	host := s.domain
	if host == "" {
		host = "this gateway"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "svkexe management shell on %s (%s)\n", host, version.Get().Short())
	fmt.Fprintf(&b, "Authenticated as %s (role: %s) by SSH key.\n", user.Email, user.Role)
	b.WriteString("This shell manages persistent Linux VMs, each running a PicoClaw coding agent.\n")
	b.WriteString("Run \"help\" for the command list, or \"help --json\" for the machine-readable\n")
	b.WriteString("catalogue of every command, argument and flag available to you.\n")
	fmt.Fprintf(&b, "One command per connection also works: ssh %s \"ls --json\".\n", host)
	b.WriteString("Enter one command per line; \"exit\" ends the session.\n")
	return b.String()
}

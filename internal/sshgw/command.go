package sshgw

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
)

// Command groups order the help output and give an agent a map of the surface
// rather than one flat list.
const (
	groupVM      = "VM lifecycle"
	groupConfig  = "VM configuration"
	groupDomains = "Custom domains"
	groupAccount = "Account"
	groupSession = "Session"
	groupAdmin   = "Administration"
)

// groupOrder is the order groups are printed in; a command in a group missing
// here would never be shown, which is what registryComplete guards against.
var groupOrder = []string{groupVM, groupConfig, groupDomains, groupAccount, groupSession, groupAdmin}

// argSpec documents one positional argument.
type argSpec struct {
	Name     string
	Desc     string
	Optional bool
	// Repeated marks an argument that swallows the rest of the line, such as
	// the body of an SSH public key.
	Repeated bool
}

// flagSpec documents one --flag. Flags are how optional settings are passed,
// because an LLM agent writing a command line has nothing to guess from
// positional ordering.
type flagSpec struct {
	Name string
	Desc string
	// Value marks a --name=value flag; without it the flag is a switch.
	Value bool
}

// subSpec is one action of a command that groups several under one word, such
// as "llm add" or "domain verify". It is a command in its own right: it carries
// its own handler, arguments and flags, so the dispatcher resolves it, the
// catalogue describes it exactly, and a flag declared for one action is not
// silently accepted by another.
type subSpec struct {
	Name  string
	Usage string
	Desc  string
	Args  []argSpec
	Flags []flagSpec
	// JSON marks an action that renders itself as JSON under --json. A writing
	// action leaves it false, so --json is refused there rather than accepted
	// and answered with prose.
	JSON bool
	Run  func(c *cmdCtx) error
}

// command is the single declaration of a menu command: it drives dispatch, the
// human help, the JSON catalogue and the admin gate at once, so a command
// cannot exist without being documented.
type command struct {
	Name        string
	Group       string
	Usage       string
	Summary     string
	Description string
	Args        []argSpec
	Flags       []flagSpec
	Subcommands []subSpec
	Examples    []string
	// Aliases are extra names that dispatch here. They are not advertised
	// separately in help.
	Aliases []string
	// AdminOnly hides the command from, and refuses it to, anyone whose role
	// is not "admin".
	AdminOnly bool
	// JSON marks a read command that renders itself as JSON under --json.
	JSON bool
	// Interactive marks a command that only means something inside the
	// interactive menu, such as exit.
	Interactive bool
	// Run is the bare form, used when no subcommand word follows. A command
	// that is nothing but a group of subcommands leaves it nil, and then the
	// word alone is a usage error listing the actions.
	Run func(c *cmdCtx) error
}

// sub finds a subcommand by the word that follows the command name.
func (cmd *command) sub(name string) *subSpec {
	for i := range cmd.Subcommands {
		if cmd.Subcommands[i].Name == name {
			return &cmd.Subcommands[i]
		}
	}
	return nil
}

// registry is every command the gateway offers and byName resolves a typed
// word, including aliases, to one of them. They are filled in by init rather
// than by a var initializer because the handlers they hold read them back —
// help is itself a command — and Go rejects that as an initialization cycle.
var (
	registry []*command
	byName   map[string]*command
)

func init() {
	registry = buildRegistry()
	byName = indexRegistry(registry)
}

func buildRegistry() []*command {
	var all []*command
	all = append(all, vmCommands()...)
	all = append(all, configCommands()...)
	all = append(all, domainCommands()...)
	all = append(all, accountCommands()...)
	all = append(all, passwordCommand())
	all = append(all, sessionCommands()...)
	all = append(all, adminCommands()...)
	return all
}

func indexRegistry(cmds []*command) map[string]*command {
	index := make(map[string]*command, len(cmds)*2)
	for _, c := range cmds {
		index[c.Name] = c
		for _, alias := range c.Aliases {
			index[alias] = c
		}
	}
	return index
}

// visibleCommands returns the commands this user may run, in group order.
func visibleCommands(user *db.User) []*command {
	admin := isAdmin(user)
	var out []*command
	for _, group := range groupOrder {
		var inGroup []*command
		for _, c := range registry {
			if c.Group == group && (admin || !c.AdminOnly) && (user == nil || user.Role != db.GuestRole || guestCommandAllowed(c.Name)) {
				inGroup = append(inGroup, c)
			}
		}
		sort.SliceStable(inGroup, func(i, j int) bool { return inGroup[i].Name < inGroup[j].Name })
		out = append(out, inGroup...)
	}
	return out
}

func isAdmin(user *db.User) bool {
	return user != nil && user.Role == "admin"
}

// cmdCtx is everything a command handler is given: the session it answers on,
// who is asking, and the parsed line.
type cmdCtx struct {
	ctx  context.Context
	s    *Server
	sess gssh.Session
	out  *out
	user *db.User
	cmd  *command
	// sub is the action that was resolved, nil for a command's bare form.
	sub *subSpec
	// args are the positional arguments after the command and action words.
	args []string
	// flags are the --name[=value] arguments, already validated against the
	// command's declared flags.
	flags map[string]string
	// json reports that --json was passed.
	json bool
	// interactive reports that this line came from the menu loop rather than
	// from a one-shot "ssh host <command>" invocation.
	interactive bool
	// pty reports that the client asked for a terminal.
	pty bool
}

func (c *cmdCtx) arg(i int) string {
	if i < len(c.args) {
		return c.args[i]
	}
	return ""
}

func (c *cmdCtx) flag(name string) string { return c.flags[name] }

func (c *cmdCtx) hasFlag(name string) bool {
	_, ok := c.flags[name]
	return ok
}

// printf writes human-facing output.
func (c *cmdCtx) printf(format string, a ...any) { c.out.printf(format, a...) }

func (c *cmdCtx) print(s string) { c.out.print(s) }

// errUnknownCommand is what a caller typed a name nothing answers to. It is a
// sentinel because a one-shot invocation reports it with its own exit status:
// "there is no such command" and "the command failed" are different answers.
var errUnknownCommand = errors.New("unknown command")

// usageError is an error that also reminds the caller of the shape of what they
// invoked. It is separate from an ordinary failure because a wrong invocation is
// the one failure where repeating the usage line actually helps — especially for
// an agent, which can correct itself from it without another round trip.
//
// The usage line is filled in by the dispatcher, which is the only place that
// knows which of a command's actions was resolved.
type usageError struct{ msg, usage string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// explain attaches the usage line of whatever was invoked to a usage error.
func explain(err error, usage string) error {
	var ue *usageError
	if errors.As(err, &ue) && ue.usage == "" {
		ue.usage = usage
	}
	return err
}

// action is one resolved invocation: which command, which of its actions, and
// the arguments and flags that action accepts.
type action struct {
	cmd   *command
	sub   *subSpec
	run   func(c *cmdCtx) error
	args  []string
	flags map[string]string
	json  bool
}

// usage is the shape to remind the caller of when they get it wrong: the
// action's own, not the whole command's.
func (a action) usage() string {
	if a.sub != nil {
		return a.sub.Usage
	}
	return a.cmd.Usage
}

// parseLine resolves a typed line to an action, splits its positional
// arguments from its flags, and validates those flags against what that action
// declares — not against the union of every action under the same word.
func parseLine(tokens []string) (action, error) {
	cmd, ok := byName[tokens[0]]
	if !ok {
		return action{}, fmt.Errorf("%w %q; type \"help\" for the command list, or \"help --json\" for a machine-readable one", errUnknownCommand, tokens[0])
	}

	a := action{cmd: cmd, run: cmd.Run, args: []string{}, flags: map[string]string{}}
	rest := tokens[1:]
	// A subcommand word is resolved before anything else, so "share list dev"
	// and "share dev" put the VM at the same argument position. "--" suppresses
	// it, which is how a VM that happens to be named after an action is still
	// reachable: "task -- retry".
	if len(rest) > 0 && rest[0] != "--" {
		if sub := cmd.sub(rest[0]); sub != nil {
			a.sub, a.run, rest = sub, sub.Run, rest[1:]
		}
	}
	if a.run == nil {
		return action{}, explain(usagef("%q needs an action: %s", cmd.Name, subcommandNames(cmd)), cmd.Usage)
	}

	allowed := map[string]flagSpec{}
	for _, f := range a.allFlags() {
		allowed[f.Name] = f
	}

	literal := false
	for _, tok := range rest {
		switch {
		case literal || !strings.HasPrefix(tok, "--"):
			a.args = append(a.args, tok)
		case tok == "--":
			literal = true
		default:
			name, value, hasValue := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
			spec, ok := allowed[name]
			if !ok {
				return action{}, explain(usagef("unknown flag --%s for %q", name, a.name()), a.usage())
			}
			if spec.Value && !hasValue {
				return action{}, explain(usagef("--%s needs a value, as --%s=...", name, name), a.usage())
			}
			if !spec.Value && hasValue {
				return action{}, explain(usagef("--%s takes no value", name), a.usage())
			}
			// Silently keeping the last one would carry out an instruction the
			// caller did not give: "new dev --cpu=4 --cpu=8" is a mistake of
			// the same kind as "--cpu=4abc", which this parser already refuses.
			if _, repeated := a.flags[name]; repeated {
				return action{}, explain(usagef("--%s is given more than once", name), a.usage())
			}
			a.flags[name] = value
		}
	}
	_, a.json = a.flags["json"]

	// An argument declared Repeated swallows the rest of the line, which is
	// what an SSH public key needs: it arrives as several tokens unless the
	// caller quoted it, and both spellings have to mean the same thing.
	declared := a.declaredArgs()
	a.args = joinRepeated(declared, a.args)

	// More arguments than the action declares is a mistake worth naming rather
	// than ignoring: on a command that groups actions it is usually a misspelt
	// action word, which would otherwise be silently answered by the bare form.
	if len(a.args) > len(declared) {
		// On a command that has both a bare form and actions, the suspect word
		// is the first one, not the first one past the bare form's arguments:
		// "share bogus dev" is a misspelt action, not a stray third argument.
		if a.sub == nil && len(a.cmd.Subcommands) > 0 {
			return action{}, explain(usagef("%q has no action %q: use %s", a.cmd.Name, a.args[0], subcommandNames(a.cmd)), a.cmd.Usage)
		}
		return action{}, explain(usagef("%s takes %s", a.name(), argumentCount(declared)), a.usage())
	}
	return a, nil
}

func argumentCount(declared []argSpec) string {
	switch len(declared) {
	case 0:
		return "no arguments"
	case 1:
		return "one argument"
	default:
		return fmt.Sprintf("at most %d arguments", len(declared))
	}
}

// name is what the caller typed, used when telling them what they got wrong.
func (a action) name() string {
	if a.sub != nil {
		return a.cmd.Name + " " + a.sub.Name
	}
	return a.cmd.Name
}

// allFlags is the action's own flags plus the --json it accepts, if it does.
func (a action) allFlags() []flagSpec {
	if a.sub != nil {
		return withJSONFlag(a.sub.Flags, a.sub.JSON)
	}
	return withJSONFlag(a.cmd.Flags, a.cmd.JSON)
}

func (a action) declaredArgs() []argSpec {
	if a.sub != nil {
		return a.sub.Args
	}
	return a.cmd.Args
}

func withJSONFlag(flags []flagSpec, json bool) []flagSpec {
	out := append([]flagSpec(nil), flags...)
	if json {
		out = append(out, flagSpec{Name: "json", Desc: "emit JSON instead of text"})
	}
	return out
}

// joinRepeated folds the tail of the arguments into the last one when that one
// is declared Repeated, so a handler reads it as the single value it is.
func joinRepeated(declared []argSpec, args []string) []string {
	last := len(declared) - 1
	if last < 0 || !declared[last].Repeated || len(args) <= last {
		return args
	}
	return append(args[:last:last], strings.Join(args[last:], " "))
}

func subcommandNames(cmd *command) string {
	names := make([]string, 0, len(cmd.Subcommands))
	for _, sub := range cmd.Subcommands {
		names = append(names, sub.Name)
	}
	return strings.Join(names, ", ")
}

// splitArgs tokenizes a command line, honouring single and double quotes so an
// argument may contain spaces — an SSH public key or a task description would
// otherwise be impossible to pass.
func splitArgs(line string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	started := false
	var quote rune

	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			if started {
				tokens = append(tokens, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced %c quote", quote)
	}
	if started {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

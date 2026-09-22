package sshgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
)

// errExitSession is how the exit command asks the menu loop to stop. It is a
// sentinel rather than a special case in the loop so that "exit" is a registry
// entry like everything else and therefore documents itself.
var errExitSession = errors.New("session ended")

// out writes command output to a session, translating line endings for the
// reader. A terminal in raw mode needs a carriage return on every line; an LLM
// agent piping the output into a parser must not be handed one.
type out struct {
	w io.Writer
	// crlf is set for sessions that asked for a PTY.
	crlf bool
}

func newOut(w io.Writer, crlf bool) *out { return &out{w: w, crlf: crlf} }

// Write implements io.Writer so a command can hand the output stream to
// anything that writes bytes.
func (o *out) Write(p []byte) (int, error) {
	if !o.crlf || !bytes.ContainsRune(p, '\n') {
		return o.w.Write(p)
	}
	// Only bare newlines are expanded; a line that already ends in CRLF is
	// left as it is so nothing acquires a second carriage return.
	var buf bytes.Buffer
	for i, b := range p {
		if b == '\n' && (i == 0 || p[i-1] != '\r') {
			buf.WriteByte('\r')
		}
		buf.WriteByte(b)
	}
	if _, err := o.w.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *out) print(s string) { io.WriteString(o, s) }

func (o *out) printf(format string, a ...any) { fmt.Fprintf(o, format, a...) }

// raw writes bytes with no line-ending translation, which is what a JSON
// document needs: a parser on the other end must not receive carriage returns.
func (o *out) raw(s string) { io.WriteString(o.w, s) }

// writeJSON emits a compact single-line JSON document. Compact rather than
// indented so that it stays one line: it is written without translation, and a
// multi-line document would stagger across a terminal in raw mode.
func (c *cmdCtx) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("render JSON: %w", err)
	}
	c.out.raw(string(data) + "\n")
	return nil
}

// exec parses one command line and runs it, returning the error the command
// failed with. It is the single path into a command: the interactive menu, a
// one-shot "ssh host <command>" invocation and the tests all come through here.
func (s *Server) exec(ctx context.Context, sess gssh.Session, o *out, user *db.User, line string, interactive, pty bool) error {
	user, err := s.currentUser(user)
	if err != nil {
		return err
	}
	tokens, err := splitArgs(line)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}

	// The role is checked before the line is parsed: a parse error names the
	// actions a command offers, and an administrator command must not describe
	// itself to somebody who may not run it.
	if cmd, known := byName[tokens[0]]; known && cmd.AdminOnly && !isAdmin(user) {
		return fmt.Errorf("%q is an administrator command", cmd.Name)
	}

	a, err := parseLine(tokens)
	if err != nil {
		return err
	}
	if user.Role == db.GuestRole && !guestCommandAllowed(a.cmd.Name) {
		return fmt.Errorf("guest accounts cannot run %q", a.cmd.Name)
	}
	if a.cmd.Interactive && !interactive {
		return fmt.Errorf("%q only means something in an interactive session", a.cmd.Name)
	}

	return explain(a.run(&cmdCtx{
		ctx:         ctx,
		s:           s,
		sess:        sess,
		out:         o,
		user:        user,
		cmd:         a.cmd,
		sub:         a.sub,
		args:        a.args,
		flags:       a.flags,
		json:        a.json,
		interactive: interactive,
		pty:         pty,
	}), a.usage())
}

// reportError renders a failed command. A usage error repeats the shape of
// what was invoked, which is the one case where doing so helps rather than nags.
func reportError(o *out, err error) {
	o.printf("Error: %v\n", err)
	var ue *usageError
	if errors.As(err, &ue) && ue.usage != "" {
		o.printf("Usage: %s\n", ue.usage)
	}
}

// runOneShot executes a single command handed to ssh on the command line and
// exits with its status. This is how a script — or an LLM agent — drives the
// gateway without having to speak to an interactive prompt.
func (s *Server) runOneShot(sess gssh.Session, user *db.User, line string) {
	_, _, pty := sess.Pty()

	if err := s.exec(sess.Context(), sess, newOut(sess, pty), user, line, false, pty); err != nil {
		reportError(newOut(sess.Stderr(), pty), err)
		sess.Exit(exitStatus(err))
		return
	}
	sess.Exit(0)
}

// exitStatus maps a failure to the status a caller reads: asking for something
// that does not exist is not the same as asking for something that failed.
func exitStatus(err error) int {
	if errors.Is(err, errUnknownCommand) {
		return exitUnknownCommand
	}
	return exitFailure
}

// Exit statuses a one-shot invocation reports. They follow shell convention so
// a caller can tell "you asked for something that does not exist" from "the
// thing you asked for failed".
const (
	exitFailure        = 1
	exitUnknownCommand = 127
)

// runMenu shows the interactive command shell on the SSH session.
func (s *Server) runMenu(sess gssh.Session, user *db.User) {
	_, _, pty := sess.Pty()
	o := newOut(sess, pty)

	if pty {
		o.raw(banner)
		o.printf("Welcome, %s\n", user.Email)
		o.print("Type \"help\" for available commands.\n\n")
	} else {
		// Nothing asked for a terminal, which is how an LLM agent or a script
		// arrives. Greet it with something it can act on rather than with an
		// ASCII banner.
		o.print(agentBrief(s, user))
	}

	ctx := sess.Context()
	le := &lineEditor{}

	for {
		if pty {
			o.raw("\rsvk ▶ ")
		}

		line, err := le.readLine(sess, pty)
		if err != nil {
			return
		}
		if line == "" {
			continue
		}

		switch err := s.exec(ctx, sess, o, user, line, true, pty); {
		case errors.Is(err, errExitSession):
			sess.Exit(0)
			return
		case err != nil:
			reportError(o, err)
		}
	}
}

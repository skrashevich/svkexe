package sshgw

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
)

const banner = "\r\n              _\r\n  _____   _| | __\r\n / __\\ \\ / / |/ /\r\n \\__ \\\\ V /|   <\r\n |___/ \\_/ |_|\\_\\\r\n\r\n"

// --- Helpers ---

// findContainer looks up one of the caller's own VMs by name.
func (c *cmdCtx) findContainer(name string) (*db.Container, error) {
	if name == "" {
		return nil, usagef("name a VM")
	}
	container, err := c.s.db.GetContainerByName(name, c.user.ID)
	if err != nil {
		return nil, fmt.Errorf("VM %q not found", name)
	}
	return container, nil
}

// findAccessibleContainer hides storage errors and does not disclose whether
// a denied VM exists. Ambiguous names keep the resolver's actionable guidance.
func (c *cmdCtx) findAccessibleContainer(name string) (*db.Container, error) {
	if name == "" {
		return nil, usagef("name a VM")
	}
	container, err := c.s.db.ResolveAccessibleContainer(name, c.user.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("VM %q not found or access denied", name)
	}
	return container, err
}

// requireRunning is the one wording for "this needs the VM up". Both the shell
// attach and the agent probe reach into a live instance, and an owner reading
// two different refusals would have to work out that they mean the same thing.
func requireRunning(container *db.Container) error {
	if !strings.EqualFold(container.Status, "running") {
		return fmt.Errorf("VM %q is not running (status: %s); start it first", container.Name, container.Status)
	}
	return nil
}

func statusIcon(status string) string {
	switch strings.ToLower(status) {
	case "running":
		return "[+]"
	case "stopped":
		return "[-]"
	default:
		return "[~]"
	}
}

func formatMB(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%dGB", mb/1024)
	}
	return fmt.Sprintf("%dMB", mb)
}

func formatGB(gb int) string {
	return fmt.Sprintf("%dGB", gb)
}

// Bounds on what one session may accumulate. A command line is at most a
// hostname, a key or a task description; anything longer is a client sending
// bytes at a gateway shared with other tenants, and both the line buffer and
// the history would otherwise grow for as long as it keeps going.
const (
	maxLineLen = 8192
	maxHistory = 200
)

// errLineTooLong ends a session that is no longer typing commands.
var errLineTooLong = fmt.Errorf("command line longer than %d bytes", maxLineLen)

// lineEditor reads a command line from the session. With a terminal it offers
// editing and history; without one it just reads a line, because a client that
// never asked for a PTY is a script or an agent whose input is already
// line-buffered and which would only be confused by echo and escape codes.
type lineEditor struct {
	history []string
	// pending holds the bytes read past the last newline, so the next line
	// starts with them instead of losing them to a fresh read.
	pending []byte
}

// remember appends to the history, dropping the oldest line once it is full.
func (le *lineEditor) remember(line string) {
	if line == "" {
		return
	}
	if len(le.history) == maxHistory {
		le.history = append(le.history[:0], le.history[1:]...)
	}
	le.history = append(le.history, line)
}

// readLine reads one command line.
func (le *lineEditor) readLine(sess gssh.Session, pty bool) (string, error) {
	if !pty {
		// No history is kept here: nothing outside a terminal can recall it,
		// and holding a session's worth of lines for a reader that will never
		// ask is a cost with no user.
		return le.readPlain(sess)
	}
	return le.readLineEdited(sess)
}

// readPlain reads a newline-terminated line, buffering what it over-reads. The
// bound is on the line being assembled rather than on any read: a client that
// never sends a newline is cut off once the buffer passes maxLineLen — plus at
// most the one chunk that carried it there — instead of growing it for as long
// as the client keeps sending.
func (le *lineEditor) readPlain(r io.Reader) (string, error) {
	chunk := make([]byte, 1024)
	for {
		if i := bytes.IndexByte(le.pending, '\n'); i >= 0 {
			line := strings.TrimSpace(string(le.pending[:i]))
			le.pending = append([]byte(nil), le.pending[i+1:]...)
			return line, nil
		}
		if len(le.pending) > maxLineLen {
			return "", errLineTooLong
		}
		n, err := r.Read(chunk)
		le.pending = append(le.pending, chunk[:n]...)
		if err != nil {
			// A last line without its newline is still a command; the error
			// comes back on the next read.
			if line := strings.TrimSpace(string(le.pending)); line != "" {
				le.pending = nil
				return line, nil
			}
			return "", err
		}
	}
}

// readLineEdited reads a line with arrow-key navigation and command history.
func (le *lineEditor) readLineEdited(sess gssh.Session) (string, error) {
	var buf []byte
	pos := 0 // cursor position within buf
	histIdx := len(le.history)
	var savedLine []byte
	b := make([]byte, 1)

	// redraw rewrites the line from the start and repositions the cursor.
	redraw := func() {
		if pos > 0 {
			fmt.Fprintf(sess, "\x1b[%dD", pos)
		}
		sess.Write(buf)
		io.WriteString(sess, "\x1b[K")
		if len(buf) > pos {
			fmt.Fprintf(sess, "\x1b[%dD", len(buf)-pos)
		}
	}

	// setLine replaces the buffer and moves cursor to end.
	setLine := func(newBuf []byte) {
		if pos > 0 {
			fmt.Fprintf(sess, "\x1b[%dD", pos)
		}
		buf = newBuf
		pos = len(buf)
		sess.Write(buf)
		io.WriteString(sess, "\x1b[K")
	}

	for {
		_, err := sess.Read(b)
		if err != nil {
			return "", err
		}
		ch := b[0]
		switch {
		case ch == '\r' || ch == '\n':
			io.WriteString(sess, "\r\n")
			line := strings.TrimSpace(string(buf))
			le.remember(line)
			return line, nil

		case ch == 127 || ch == 8: // backspace
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
				io.WriteString(sess, "\x1b[D") // sync screen cursor with new pos
				redraw()
			}

		case ch == 3: // Ctrl-C
			io.WriteString(sess, "^C\r\n")
			return "", nil

		case ch == 4: // Ctrl-D
			if len(buf) == 0 {
				io.WriteString(sess, "\r\n")
				return "exit", nil
			}

		case ch == 1: // Ctrl-A — move to start
			if pos > 0 {
				fmt.Fprintf(sess, "\x1b[%dD", pos)
				pos = 0
			}

		case ch == 5: // Ctrl-E — move to end
			if pos < len(buf) {
				fmt.Fprintf(sess, "\x1b[%dC", len(buf)-pos)
				pos = len(buf)
			}

		case ch == 21: // Ctrl-U — clear line
			if len(buf) > 0 {
				if pos > 0 {
					fmt.Fprintf(sess, "\x1b[%dD", pos)
				}
				io.WriteString(sess, "\x1b[K")
				buf = nil
				pos = 0
			}

		case ch == 0x1b: // ESC — parse escape sequence
			if _, err := sess.Read(b); err != nil {
				return "", err
			}
			if b[0] != '[' {
				continue
			}
			if _, err := sess.Read(b); err != nil {
				return "", err
			}
			switch b[0] {
			case 'A': // Up arrow
				if histIdx > 0 {
					if histIdx == len(le.history) {
						savedLine = make([]byte, len(buf))
						copy(savedLine, buf)
					}
					histIdx--
					setLine([]byte(le.history[histIdx]))
				}
			case 'B': // Down arrow
				if histIdx < len(le.history) {
					histIdx++
					if histIdx == len(le.history) {
						setLine(savedLine)
					} else {
						setLine([]byte(le.history[histIdx]))
					}
				}
			case 'C': // Right arrow
				if pos < len(buf) {
					io.WriteString(sess, "\x1b[C")
					pos++
				}
			case 'D': // Left arrow
				if pos > 0 {
					io.WriteString(sess, "\x1b[D")
					pos--
				}
			case 'H': // Home
				if pos > 0 {
					fmt.Fprintf(sess, "\x1b[%dD", pos)
					pos = 0
				}
			case 'F': // End
				if pos < len(buf) {
					fmt.Fprintf(sess, "\x1b[%dC", len(buf)-pos)
					pos = len(buf)
				}
			case '3': // Delete key: \x1b[3~
				if _, err := sess.Read(b); err != nil {
					return "", err
				}
				if b[0] != '~' {
					continue
				}
				if pos < len(buf) {
					buf = append(buf[:pos], buf[pos+1:]...)
					redraw()
				}
			}

		case ch >= 32 && ch < 127: // printable ASCII
			if len(buf) >= maxLineLen {
				return "", errLineTooLong
			}
			if pos == len(buf) {
				buf = append(buf, ch)
				pos++
				sess.Write([]byte{ch})
			} else {
				buf = append(buf, 0)
				copy(buf[pos+1:], buf[pos:])
				buf[pos] = ch
				pos++
				// Write from new char to end, then move cursor back.
				sess.Write(buf[pos-1:])
				fmt.Fprintf(sess, "\x1b[%dD", len(buf)-pos)
			}
		}
	}
}

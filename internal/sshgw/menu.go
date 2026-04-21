package sshgw

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/svkexe/platform/internal/db"
	"github.com/svkexe/platform/internal/runtime"
)

const banner = `
 _____ _   ____  _____ _  __ _____  _____
|  ___| | / /\ \/ / __| |/ /| ____||_   _|
| |_  | |/ /  \  /|  _|| ' / |  _|   | |
|  _| |   <   / / | |__| . \ | |___  | |
|_|   |_|\_\ /_/  |____|_|\_\|_____| |_|

`

// runMenu shows an interactive VM selection menu on the SSH session.
func (s *Server) runMenu(sess gssh.Session, user *db.User) {
	io.WriteString(sess, banner)
	fmt.Fprintf(sess, "Welcome, %s\n\n", user.Email)

	ctx := context.Background()

	for {
		containers, err := s.db.ListContainersByOwner(user.ID)
		if err != nil {
			fmt.Fprintf(sess.Stderr(), "error listing VMs: %v\n", err)
			sess.Exit(1)
			return
		}

		if len(containers) == 0 {
			io.WriteString(sess, "No VMs found. Create a VM from the dashboard first.\n")
			sess.Exit(0)
			return
		}

		io.WriteString(sess, "Your VMs:\n\n")
		for i, c := range containers {
			status := c.Status
			statusIcon := statusIcon(status)
			fmt.Fprintf(sess, "  [%d] %s  %s %s\n", i+1, c.Name, statusIcon, status)
		}
		io.WriteString(sess, "\n  [q] Quit\n\n")
		io.WriteString(sess, "Select a VM: ")

		line, err := readLine(sess)
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)

		if line == "q" || line == "Q" || line == "quit" || line == "exit" {
			io.WriteString(sess, "Goodbye.\n")
			sess.Exit(0)
			return
		}

		// Parse selection index.
		var idx int
		if _, err := fmt.Sscanf(line, "%d", &idx); err != nil || idx < 1 || idx > len(containers) {
			io.WriteString(sess, "Invalid selection.\n\n")
			continue
		}

		target := containers[idx-1]

		if target.Status != "running" {
			fmt.Fprintf(sess, "VM %q is not running (status: %s). Start it from the dashboard.\n\n", target.Name, target.Status)
			continue
		}

		sr, ok := s.runtime.(runtime.ShellRuntime)
		if !ok {
			io.WriteString(sess.Stderr(), "runtime does not support interactive sessions\n")
			sess.Exit(1)
			return
		}

		fmt.Fprintf(sess, "Connecting to %s...\n", target.Name)

		ptyReq, winCh, isPTY := sess.Pty()

		var initialCols, initialRows uint16
		if isPTY {
			initialCols = uint16(ptyReq.Window.Width)
			initialRows = uint16(ptyReq.Window.Height)
		} else {
			initialCols = 80
			initialRows = 24
		}

		resizeCh := make(chan runtime.ResizeEvent, 4)
		doneCh := make(chan struct{})

		if isPTY {
			go func() {
				for win := range winCh {
					select {
					case resizeCh <- runtime.ResizeEvent{Cols: uint16(win.Width), Rows: uint16(win.Height)}:
					default:
					}
				}
				close(resizeCh)
			}()
		} else {
			close(resizeCh)
		}

		opts := runtime.ExecInteractiveOpts{
			IncusName:   target.IncusName,
			Command:     []string{"/bin/bash", "-l"},
			Stdin:       sess,
			Stdout:      sess,
			InitialCols: initialCols,
			InitialRows: initialRows,
			Resize:      resizeCh,
			Done:        doneCh,
		}

		if err := sr.ExecInteractive(ctx, opts); err != nil {
			fmt.Fprintf(sess.Stderr(), "exec error: %v\n", err)
		}

		// After session ends, loop back to menu.
		io.WriteString(sess, "\nSession ended. Press Enter to return to menu.\n")
		readLine(sess)
	}
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

// readLine reads a line from the SSH session (handles both raw PTY and line-buffered input).
func readLine(r io.Reader) (string, error) {
	scanner := bufio.NewReader(r)
	line, err := scanner.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	return line, err
}

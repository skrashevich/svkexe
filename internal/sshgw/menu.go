package sshgw

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

const banner = "\r\n              _\r\n  _____   _| | __\r\n / __\\ \\ / / |/ /\r\n \\__ \\\\ V /|   <\r\n |___/ \\_/ |_|\\_\\\r\n\r\n"

const helpText = "\r\nSVK commands:\r\n\r\n" +
	"  help                  - Show help information\r\n" +
	"  ls                    - List your VMs\r\n" +
	"  new <name>            - Create a new VM\r\n" +
	"  rm <name>             - Delete a VM\r\n" +
	"  start <name>          - Start a VM\r\n" +
	"  stop <name>           - Stop a VM\r\n" +
	"  restart <name>        - Restart a VM\r\n" +
	"  rename <old> <new>    - Rename a VM\r\n" +
	"  stat <name>           - Show VM details\r\n" +
	"  ssh <name>            - SSH into a VM\r\n" +
	"  whoami                - Show your user information\r\n" +
	"  ssh-key               - Manage SSH keys\r\n" +
	"    ssh-key list          List all SSH keys\r\n" +
	"    ssh-key remove <name> Remove an SSH key\r\n" +
	"  exit                  - Exit\r\n\r\n"

var validVMName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// runMenu shows an interactive command shell on the SSH session.
func (s *Server) runMenu(sess gssh.Session, user *db.User) {
	io.WriteString(sess, banner)
	fmt.Fprintf(sess, "Welcome, %s\r\n", user.Email)
	io.WriteString(sess, "Type \"help\" for available commands.\r\n\r\n")

	ctx := sess.Context()

	for {
		io.WriteString(sess, "svk ▶ ")

		line, err := readLine(sess)
		if err != nil {
			return
		}

		args := strings.Fields(line)
		if len(args) == 0 {
			continue
		}

		cmd := args[0]
		params := args[1:]

		switch cmd {
		case "help":
			io.WriteString(sess, helpText)
		case "ls":
			s.cmdLs(sess, user)
		case "new":
			s.cmdNew(ctx, sess, user, params)
		case "rm":
			s.cmdRm(ctx, sess, user, params)
		case "start":
			s.cmdStart(ctx, sess, user, params)
		case "stop":
			s.cmdStop(ctx, sess, user, params)
		case "restart":
			s.cmdRestart(ctx, sess, user, params)
		case "rename":
			s.cmdRename(sess, user, params)
		case "stat":
			s.cmdStat(sess, user, params)
		case "ssh":
			s.cmdSSH(ctx, sess, user, params)
		case "whoami":
			s.cmdWhoami(sess, user)
		case "ssh-key":
			s.cmdSSHKey(sess, user, params)
		case "exit", "quit":
			io.WriteString(sess, "Goodbye.\r\n")
			sess.Exit(0)
			return
		default:
			fmt.Fprintf(sess, "Unknown command: %s. Type \"help\" for available commands.\r\n", cmd)
		}
	}
}

// --- Commands ---

func (s *Server) cmdLs(sess gssh.Session, user *db.User) {
	containers, err := s.db.ListContainersByOwner(user.ID)
	if err != nil {
		fmt.Fprintf(sess, "Error: %v\r\n", err)
		return
	}
	if len(containers) == 0 {
		io.WriteString(sess, "No VMs found.\r\n")
		return
	}
	io.WriteString(sess, "\r\n")
	// Header
	fmt.Fprintf(sess, "  %-20s %-10s %-6s %-8s %-6s\r\n", "NAME", "STATUS", "CPU", "MEMORY", "DISK")
	fmt.Fprintf(sess, "  %-20s %-10s %-6s %-8s %-6s\r\n", "----", "------", "---", "------", "----")
	for _, c := range containers {
		icon := statusIcon(c.Status)
		fmt.Fprintf(sess, "  %-20s %s %-7s %-6d %-8s %-6s\r\n",
			c.Name,
			icon,
			c.Status,
			c.CPULimit,
			formatMB(c.MemoryMB),
			formatGB(c.DiskGB),
		)
	}
	io.WriteString(sess, "\r\n")
}

func (s *Server) cmdNew(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: new <name>\r\n")
		return
	}
	name := params[0]

	if !validVMName.MatchString(name) {
		io.WriteString(sess, "Error: invalid VM name. Use letters, digits, dots, hyphens, underscores (1-63 chars).\r\n")
		return
	}

	// Check for duplicate name.
	if _, err := s.db.GetContainerByName(name, user.ID); err == nil {
		fmt.Fprintf(sess, "Error: VM %q already exists.\r\n", name)
		return
	}

	fmt.Fprintf(sess, "Creating VM %q...\r\n", name)

	rtContainer, err := s.runtime.Create(ctx, runtime.CreateOpts{
		Name:     name,
		OwnerID:  user.ID,
		Image:    "svkexe-base",
		CPULimit: 2,
		MemoryMB: 2048,
		DiskGB:   10,
	})
	if err != nil {
		fmt.Fprintf(sess, "Error creating VM: %v\r\n", err)
		return
	}

	dbContainer := &db.Container{
		ID:        uuid.New().String(),
		Name:      name,
		OwnerID:   user.ID,
		IncusName: rtContainer.Name,
		Status:    rtContainer.Status,
		IPAddress: rtContainer.IP,
		CPULimit:  2,
		MemoryMB:  2048,
		DiskGB:    10,
	}
	if err := s.db.CreateContainer(dbContainer); err != nil {
		fmt.Fprintf(sess, "Error saving VM: %v\r\n", err)
		return
	}

	fmt.Fprintf(sess, "VM %q created.\r\n", name)
}

func (s *Server) cmdRm(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: rm <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}

	if c.Status == "running" {
		fmt.Fprintf(sess, "Stopping VM %q...\r\n", c.Name)
		if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
			fmt.Fprintf(sess, "Error stopping VM: %v\r\n", err)
			return
		}
	}

	fmt.Fprintf(sess, "Deleting VM %q...\r\n", c.Name)
	if err := s.runtime.Delete(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error deleting VM: %v\r\n", err)
		return
	}
	if err := s.db.DeleteContainer(c.ID); err != nil {
		fmt.Fprintf(sess, "Error removing VM record: %v\r\n", err)
		return
	}

	fmt.Fprintf(sess, "VM %q deleted.\r\n", c.Name)
}

func (s *Server) cmdStart(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: start <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}
	if c.Status == "running" {
		fmt.Fprintf(sess, "VM %q is already running.\r\n", c.Name)
		return
	}

	fmt.Fprintf(sess, "Starting VM %q...\r\n", c.Name)
	if err := s.runtime.Start(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error: %v\r\n", err)
		return
	}
	_ = s.db.UpdateContainerStatus(c.ID, "running", c.IPAddress)
	fmt.Fprintf(sess, "VM %q started.\r\n", c.Name)
}

func (s *Server) cmdStop(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: stop <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}
	if c.Status == "stopped" {
		fmt.Fprintf(sess, "VM %q is already stopped.\r\n", c.Name)
		return
	}

	fmt.Fprintf(sess, "Stopping VM %q...\r\n", c.Name)
	if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error: %v\r\n", err)
		return
	}
	_ = s.db.UpdateContainerStatus(c.ID, "stopped", c.IPAddress)
	fmt.Fprintf(sess, "VM %q stopped.\r\n", c.Name)
}

func (s *Server) cmdRestart(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: restart <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}

	if c.Status == "running" {
		fmt.Fprintf(sess, "Stopping VM %q...\r\n", c.Name)
		if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
			fmt.Fprintf(sess, "Error stopping: %v\r\n", err)
			return
		}
	}

	fmt.Fprintf(sess, "Starting VM %q...\r\n", c.Name)
	if err := s.runtime.Start(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error starting: %v\r\n", err)
		return
	}
	_ = s.db.UpdateContainerStatus(c.ID, "running", c.IPAddress)
	fmt.Fprintf(sess, "VM %q restarted.\r\n", c.Name)
}

func (s *Server) cmdRename(sess gssh.Session, user *db.User, params []string) {
	if len(params) < 2 {
		io.WriteString(sess, "Usage: rename <old-name> <new-name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}

	newName := params[1]
	if !validVMName.MatchString(newName) {
		io.WriteString(sess, "Error: invalid VM name. Use letters, digits, dots, hyphens, underscores (1-63 chars).\r\n")
		return
	}
	if _, err := s.db.GetContainerByName(newName, user.ID); err == nil {
		fmt.Fprintf(sess, "Error: VM %q already exists.\r\n", newName)
		return
	}

	if err := s.db.RenameContainer(c.ID, newName); err != nil {
		fmt.Fprintf(sess, "Error: %v\r\n", err)
		return
	}
	fmt.Fprintf(sess, "VM %q renamed to %q.\r\n", params[0], newName)
}

func (s *Server) cmdStat(sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: stat <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}

	io.WriteString(sess, "\r\n")
	fmt.Fprintf(sess, "  Name:       %s\r\n", c.Name)
	fmt.Fprintf(sess, "  Status:     %s %s\r\n", statusIcon(c.Status), c.Status)
	fmt.Fprintf(sess, "  CPU:        %d cores\r\n", c.CPULimit)
	fmt.Fprintf(sess, "  Memory:     %s\r\n", formatMB(c.MemoryMB))
	fmt.Fprintf(sess, "  Disk:       %s\r\n", formatGB(c.DiskGB))
	if c.IPAddress != "" {
		fmt.Fprintf(sess, "  IP:         %s\r\n", c.IPAddress)
	}
	fmt.Fprintf(sess, "  Created:    %s\r\n", c.CreatedAt.Format("2006-01-02 15:04"))
	io.WriteString(sess, "\r\n")
}

func (s *Server) cmdSSH(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: ssh <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}
	if c.Status != "running" {
		fmt.Fprintf(sess, "VM %q is not running (status: %s). Use \"start %s\" first.\r\n", c.Name, c.Status, c.Name)
		return
	}

	sr, ok := s.runtime.(runtime.ShellRuntime)
	if !ok {
		io.WriteString(sess, "Error: runtime does not support interactive sessions.\r\n")
		return
	}

	fmt.Fprintf(sess, "Connecting to %s...\r\n", c.Name)

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
		IncusName:   c.IncusName,
		Command:     []string{"/bin/bash", "-l"},
		Stdin:       sess,
		Stdout:      sess,
		InitialCols: initialCols,
		InitialRows: initialRows,
		Resize:      resizeCh,
		Done:        doneCh,
	}

	if err := sr.ExecInteractive(ctx, opts); err != nil {
		fmt.Fprintf(sess, "Exec error: %v\r\n", err)
	} else {
		select {
		case <-doneCh:
		case <-ctx.Done():
		}
	}

	io.WriteString(sess, "\r\nSession ended.\r\n")
}

func (s *Server) cmdWhoami(sess gssh.Session, user *db.User) {
	io.WriteString(sess, "\r\n")
	fmt.Fprintf(sess, "  Email:    %s\r\n", user.Email)
	if user.DisplayName != "" {
		fmt.Fprintf(sess, "  Name:     %s\r\n", user.DisplayName)
	}
	fmt.Fprintf(sess, "  Role:     %s\r\n", user.Role)
	fmt.Fprintf(sess, "  Created:  %s\r\n", user.CreatedAt.Format("2006-01-02 15:04"))

	keys, err := s.db.ListSSHKeysByUser(user.ID)
	if err == nil && len(keys) > 0 {
		io.WriteString(sess, "\r\n  SSH Keys:\r\n")
		for _, k := range keys {
			name := k.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(sess, "    %s  %s\r\n", name, k.Fingerprint)
		}
	}
	io.WriteString(sess, "\r\n")
}

func (s *Server) cmdSSHKey(sess gssh.Session, user *db.User, params []string) {
	if len(params) == 0 {
		io.WriteString(sess, "Usage:\r\n")
		io.WriteString(sess, "  ssh-key list              List all SSH keys\r\n")
		io.WriteString(sess, "  ssh-key remove <name>     Remove an SSH key\r\n")
		return
	}

	switch params[0] {
	case "list":
		keys, err := s.db.ListSSHKeysByUser(user.ID)
		if err != nil {
			fmt.Fprintf(sess, "Error: %v\r\n", err)
			return
		}
		if len(keys) == 0 {
			io.WriteString(sess, "No SSH keys found.\r\n")
			return
		}
		io.WriteString(sess, "\r\n")
		for _, k := range keys {
			name := k.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(sess, "  %-20s %s\r\n", name, k.Fingerprint)
		}
		io.WriteString(sess, "\r\n")

	case "remove":
		if len(params) < 2 {
			io.WriteString(sess, "Usage: ssh-key remove <name>\r\n")
			return
		}
		keyName := params[1]
		keys, err := s.db.ListSSHKeysByUser(user.ID)
		if err != nil {
			fmt.Fprintf(sess, "Error: %v\r\n", err)
			return
		}
		var target *db.SSHKey
		for _, k := range keys {
			if k.Name == keyName {
				target = k
				break
			}
		}
		if target == nil {
			fmt.Fprintf(sess, "SSH key %q not found.\r\n", keyName)
			return
		}
		if err := s.db.DeleteSSHKey(target.ID, user.ID); err != nil {
			fmt.Fprintf(sess, "Error: %v\r\n", err)
			return
		}
		fmt.Fprintf(sess, "SSH key %q removed.\r\n", keyName)

	default:
		fmt.Fprintf(sess, "Unknown ssh-key command: %s\r\n", params[0])
	}
}

// --- Helpers ---

// findContainer looks up a container by name for the given user.
// Writes an error to sess and returns nil if not found.
func (s *Server) findContainer(sess gssh.Session, user *db.User, name string) *db.Container {
	c, err := s.db.GetContainerByName(name, user.ID)
	if err != nil {
		fmt.Fprintf(sess, "VM %q not found.\r\n", name)
		return nil
	}
	return c
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

// readLine reads a line of input from the SSH session with basic line editing
// (backspace support). Returns the trimmed line on Enter, or error on EOF.
func readLine(sess gssh.Session) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		_, err := sess.Read(b)
		if err != nil {
			return "", err
		}
		ch := b[0]
		switch {
		case ch == '\r' || ch == '\n':
			io.WriteString(sess, "\r\n")
			return strings.TrimSpace(string(buf)), nil
		case ch == 127 || ch == 8: // backspace / delete
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				io.WriteString(sess, "\b \b")
			}
		case ch == 3: // Ctrl-C
			io.WriteString(sess, "^C\r\n")
			return "", nil
		case ch == 4: // Ctrl-D
			if len(buf) == 0 {
				io.WriteString(sess, "\r\n")
				return "exit", nil
			}
		case ch == 0x1b: // ESC — consume escape sequence (e.g. arrow keys)
			sess.Read(b) // '[' or other
			sess.Read(b) // 'A'/'B'/'C'/'D' etc.
		case ch >= 32 && ch < 127: // printable ASCII
			buf = append(buf, ch)
			sess.Write([]byte{ch})
		}
	}
}


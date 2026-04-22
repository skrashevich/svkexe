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
	"github.com/skrashevich/svkexe/internal/shelley"
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
	"  recreate <name>       - Recreate VM from latest image (preserves /data)\r\n" +
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
	le := &lineEditor{}

	for {
		io.WriteString(sess, "svk ▶ ")

		line, err := le.readLine(sess)
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
		case "recreate":
			s.cmdRecreate(ctx, sess, user, params)
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

	env := map[string]string{}
	if isPTY {
		if ptyReq.Term != "" {
			env["TERM"] = ptyReq.Term
		} else {
			env["TERM"] = "xterm-256color"
		}
	}

	opts := runtime.ExecInteractiveOpts{
		IncusName:   c.IncusName,
		Command:     []string{"/bin/bash", "-l"},
		Env:         env,
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

func (s *Server) cmdRecreate(ctx context.Context, sess gssh.Session, user *db.User, params []string) {
	if len(params) < 1 {
		io.WriteString(sess, "Usage: recreate <name>\r\n")
		return
	}
	c := s.findContainer(sess, user, params[0])
	if c == nil {
		return
	}
	if c.Status == "creating" || c.Status == "recreating" {
		fmt.Fprintf(sess, "VM %q is busy (status: %s). Please wait.\r\n", c.Name, c.Status)
		return
	}

	_ = s.db.UpdateContainerStatus(c.ID, "recreating", c.IPAddress)

	// Start the container if stopped so we can back up /data.
	wasRunning := c.Status == "running"
	if c.Status == "stopped" {
		fmt.Fprintf(sess, "Starting %q for backup...\r\n", c.Name)
		if err := s.runtime.Start(ctx, c.IncusName); err == nil {
			wasRunning = true
		}
	}

	// Back up /data.
	var backupData []byte
	if wasRunning {
		if fr, ok := s.runtime.(runtime.FileRuntime); ok {
			fmt.Fprintf(sess, "Backing up /data from %q...\r\n", c.Name)
			if _, err := s.runtime.Exec(ctx, c.IncusName, []string{"tar", "-czf", "/tmp/data-backup.tar.gz", "-C", "/", "data"}); err == nil {
				backupData, _ = fr.PullFile(ctx, c.IncusName, "/tmp/data-backup.tar.gz")
			}
			if len(backupData) > 0 {
				fmt.Fprintf(sess, "Backup complete (%d bytes).\r\n", len(backupData))
			} else {
				io.WriteString(sess, "Warning: /data backup is empty (no user data found).\r\n")
			}
		}
	}

	// Stop the container.
	if wasRunning {
		fmt.Fprintf(sess, "Stopping %q...\r\n", c.Name)
		if err := s.runtime.Stop(ctx, c.IncusName); err != nil {
			fmt.Fprintf(sess, "Error stopping VM: %v\r\n", err)
			return
		}
	}

	// Delete old container.
	fmt.Fprintf(sess, "Deleting old container...\r\n")
	if err := s.runtime.Delete(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error deleting VM: %v\r\n", err)
		return
	}

	// Create new container from fresh image.
	fmt.Fprintf(sess, "Creating new container from %s image...\r\n", shelley.DefaultImage)
	_, err := s.runtime.Create(ctx, runtime.CreateOpts{
		Name:     c.Name,
		OwnerID:  user.ID,
		Image:    shelley.DefaultImage,
		CPULimit: c.CPULimit,
		MemoryMB: c.MemoryMB,
		DiskGB:   c.DiskGB,
	})
	if err != nil {
		fmt.Fprintf(sess, "Error creating VM: %v\r\n", err)
		_ = s.db.UpdateContainerStatus(c.ID, "error", "")
		return
	}

	// Start the new container.
	fmt.Fprintf(sess, "Starting %q...\r\n", c.Name)
	if err := s.runtime.Start(ctx, c.IncusName); err != nil {
		fmt.Fprintf(sess, "Error starting VM: %v\r\n", err)
		_ = s.db.UpdateContainerStatus(c.ID, "stopped", "")
		return
	}

	// Fetch IP.
	ip := ""
	if rtc, err := s.runtime.Get(ctx, c.IncusName); err == nil {
		ip = rtc.IP
	}

	// Shelley setup.
	if s.materializer != nil {
		fmt.Fprintf(sess, "Setting up Shelley...\r\n")
		if err := shelley.SetupContainer(ctx, s.runtime, s.materializer, c.ID, user.ID, s.shelleyLLMCfg); err != nil {
			fmt.Fprintf(sess, "Warning: Shelley setup failed: %v\r\n", err)
		}
	}

	// Restore /data.
	if len(backupData) > 0 {
		if fr, ok := s.runtime.(runtime.FileRuntime); ok {
			fmt.Fprintf(sess, "Restoring /data...\r\n")
			if err := fr.PushFile(ctx, c.IncusName, "/tmp/data-backup.tar.gz", backupData); err == nil {
				s.runtime.Exec(ctx, c.IncusName, []string{"tar", "-xzf", "/tmp/data-backup.tar.gz", "-C", "/"})
				s.runtime.Exec(ctx, c.IncusName, []string{"rm", "-f", "/tmp/data-backup.tar.gz"})
				s.runtime.Exec(ctx, c.IncusName, []string{"chown", "-R", "user:user", "/data"})
				io.WriteString(sess, "Data restored.\r\n")
			} else {
				fmt.Fprintf(sess, "Warning: failed to restore data: %v\r\n", err)
			}
		}
	}

	_ = s.db.UpdateContainerStatus(c.ID, "running", ip)
	fmt.Fprintf(sess, "VM %q recreated successfully.\r\n", c.Name)
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

// lineEditor provides line editing with command history for the SSH menu.
type lineEditor struct {
	history []string
}

// readLine reads a line of input with arrow-key navigation and command history.
func (le *lineEditor) readLine(sess gssh.Session) (string, error) {
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
			if line != "" {
				le.history = append(le.history, line)
			}
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


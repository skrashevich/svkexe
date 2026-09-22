package sshgw

import (
	"fmt"

	"github.com/skrashevich/svkexe/internal/db"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

func guestCommandAllowed(name string) bool {
	switch name {
	case "help", "ls", "stat", "ssh", "whoami", "ssh-key", "passwd", "exit", "clear":
		return true
	}
	return false
}

func passwordCommand() *command {
	return &command{Name: "passwd", Group: groupAccount, Usage: "passwd", Summary: "Set your web login password over SSH", Description: "Requires a terminal. Prompts twice without echoing the password; then signs in on the web using your account email.", Examples: []string{"passwd"}, Run: cmdPassword}
}
func cmdPassword(c *cmdCtx) error {
	if !c.pty || c.sess == nil {
		return fmt.Errorf("use a terminal: ssh -t -p 2222 svkexe@<gateway> passwd")
	}
	terminal := term.NewTerminal(c.sess, "")
	password, err := terminal.ReadPassword("New web password: ")
	if err != nil {
		return err
	}
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("password must be 8–72 bytes")
	}
	confirm, err := terminal.ReadPassword("Repeat password: ")
	if err != nil {
		return err
	}
	if password != confirm {
		return fmt.Errorf("passwords do not match")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	if err = c.s.db.SetUserPassword(c.user.ID, string(hash)); err != nil {
		return err
	}
	c.printf("Web password set. Sign in with %s.\n", c.user.Email)
	return nil
}

// Refresh account state on each menu command, including long-lived connections.
func (s *Server) currentUser(user *db.User) (*db.User, error) {
	if user == nil {
		return nil, fmt.Errorf("authentication required")
	}
	current, err := s.db.GetUserByID(user.ID)
	if err != nil {
		return nil, fmt.Errorf("account no longer available")
	}
	return current, nil
}

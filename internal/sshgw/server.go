package sshgw

import (
	"fmt"
	"io"
	"log"
	"net"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	gossh "golang.org/x/crypto/ssh"
)

// Server is the SSH gateway server.
type Server struct {
	db      *db.DB
	runtime runtime.ContainerRuntime
	srv     *gssh.Server
}

// New creates a new SSH gateway server bound to addr using hostKey.
func New(addr string, hostKey gossh.Signer, database *db.DB, rt runtime.ContainerRuntime) *Server {
	s := &Server{
		db:      database,
		runtime: rt,
	}

	s.srv = &gssh.Server{
		Addr:    addr,
		Handler: s.handleSession,
		PublicKeyHandler: func(ctx gssh.Context, key gssh.PublicKey) bool {
			fp := gossh.FingerprintSHA256(key)
			user, err := database.GetUserBySSHFingerprint(fp)
			if err != nil {
				return false
			}
			ctx.SetValue(ctxUser{}, user)
			return true
		},
		HostSigners: []gssh.Signer{hostKey},
	}

	return s
}

type ctxUser struct{}

// ListenAndServe starts the SSH server. It blocks until the server stops.
func (s *Server) ListenAndServe() error {
	log.Printf("SSH gateway listening on %s", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Serve accepts connections on the given listener.
func (s *Server) Serve(l net.Listener) error {
	return s.srv.Serve(l)
}

// Close shuts down the server.
func (s *Server) Close() error {
	return s.srv.Close()
}

func (s *Server) handleSession(sess gssh.Session) {
	user, _ := sess.Context().Value(ctxUser{}).(*db.User)
	if user == nil {
		fmt.Fprintln(sess.Stderr(), "authentication error")
		sess.Exit(1)
		return
	}

	// SSH username determines routing:
	// - empty or "svkexe": show interactive menu
	// - otherwise: treat as VM name / incus name and connect directly
	target := sess.User()
	if target == "" || target == "svkexe" {
		s.runMenu(sess, user)
		return
	}

	s.connectToVM(sess, user, target)
}

func (s *Server) connectToVM(sess gssh.Session, user *db.User, vmName string) {
	ctx := sess.Context()

	// Look up the container by incus name (or by name in DB).
	containers, err := s.db.ListContainersByOwner(user.ID)
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "error listing VMs: %v\n", err)
		sess.Exit(1)
		return
	}

	var target *db.Container
	for _, c := range containers {
		if c.IncusName == vmName || c.Name == vmName {
			target = c
			break
		}
	}
	if target == nil {
		fmt.Fprintf(sess.Stderr(), "VM %q not found\n", vmName)
		sess.Exit(1)
		return
	}

	if target.Status != "running" {
		fmt.Fprintf(sess.Stderr(), "VM %q is not running (status: %s)\n", target.Name, target.Status)
		sess.Exit(1)
		return
	}

	sr, ok := s.runtime.(runtime.ShellRuntime)
	if !ok {
		fmt.Fprintln(sess.Stderr(), "runtime does not support interactive sessions")
		sess.Exit(1)
		return
	}

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

	cmd := []string{"/bin/bash"}
	if isPTY {
		cmd = []string{"/bin/bash", "-l"}
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
		IncusName:   target.IncusName,
		Command:     cmd,
		Env:         env,
		Stdin:       sess,
		Stdout:      sess,
		InitialCols: initialCols,
		InitialRows: initialRows,
		Resize:      resizeCh,
		Done:        doneCh,
	}

	if err := sr.ExecInteractive(ctx, opts); err != nil {
		io.WriteString(sess.Stderr(), fmt.Sprintf("exec error: %v\n", err))
		sess.Exit(1)
		return
	}
	select {
	case <-doneCh:
	case <-ctx.Done():
	}
	sess.Exit(0)
}

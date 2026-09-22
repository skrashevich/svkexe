package sshgw

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
	"github.com/skrashevich/svkexe/internal/updater"
	gossh "golang.org/x/crypto/ssh"
)

// Config carries everything the SSH gateway needs. It is a struct rather than a
// parameter list because the menu now reaches the same management surface as
// the dashboard, and that takes as many dependencies as the HTTP server.
type Config struct {
	Addr    string
	HostKey gossh.Signer
	DB      *db.DB
	Runtime runtime.ContainerRuntime
	// Materializer may be nil, in which case VMs are created without agent
	// configuration.
	Materializer *secrets.Materializer
	PicoclawLLM  *picoclaw.LLMProxyConfig
	// EncKey decrypts the owner's stored LLM credentials.
	EncKey []byte
	// Domain is the gateway's base domain, used to build the addresses the
	// menu prints.
	Domain string
	// Aliases may be nil in a deployment without a domain; the custom-domain
	// commands then refuse rather than panic.
	Aliases *aliases.Manager
	// Updater may be nil, in which case the administrator update commands say
	// so instead of pretending to work.
	Updater *updater.Service
}

// Server is the SSH gateway server.
type Server struct {
	db             *db.DB
	runtime        runtime.ContainerRuntime
	materializer   *secrets.Materializer
	picoclawLLMCfg *picoclaw.LLMProxyConfig
	encKey         []byte
	domain         string
	aliases        *aliases.Manager
	updater        *updater.Service
	srv            *gssh.Server
}

// New creates a new SSH gateway server.
func New(cfg Config) *Server {
	s := &Server{
		db:             cfg.DB,
		runtime:        cfg.Runtime,
		materializer:   cfg.Materializer,
		picoclawLLMCfg: cfg.PicoclawLLM,
		encKey:         cfg.EncKey,
		domain:         cfg.Domain,
		aliases:        cfg.Aliases,
		updater:        cfg.Updater,
	}

	s.srv = &gssh.Server{
		Addr: cfg.Addr,
		// A session that stops sending is eventually dropped: a connection held
		// open costs the gateway a goroutine and a file descriptor, and every
		// tenant shares them. The idle window is generous because a legitimate
		// session is often a shell inside a VM with nothing being typed, and
		// MaxTimeout is absent for the same reason — a day-long shell is normal
		// work, not abuse.
		IdleTimeout: sessionIdleTimeout,
		Handler:     s.handleSession,
		PublicKeyHandler: func(ctx gssh.Context, key gssh.PublicKey) bool {
			fp := gossh.FingerprintSHA256(key)
			user, err := cfg.DB.GetUserBySSHFingerprint(fp)
			if err != nil {
				return false
			}
			ctx.SetValue(ctxUser{}, user)
			return true
		},
		HostSigners: []gssh.Signer{cfg.HostKey},
	}

	return s
}

type ctxUser struct{}

// sessionIdleTimeout is how long a connection may sit without traffic before
// the gateway closes it.
const sessionIdleTimeout = 2 * time.Hour

// menuLogin always means the management menu, whatever VMs the caller owns. It
// is the one login that is reserved, because otherwise an owner whose VM is
// named after their own account would have no way left to reach the menu.
const menuLogin = "svkexe"

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

// handleSession decides what an authenticated connection gets.
//
// The SSH login is a shortcut, not a credential: the key already identified the
// user. A login naming an owned or explicitly granted VM goes straight into it, and
// everything else — any name at all, including the one the client picked up
// from the local username — lands on the management menu. Refusing an unknown
// login would mean a user whose local account is not one of their VM names
// could not reach the gateway without knowing a magic word.
func (s *Server) handleSession(sess gssh.Session) {
	user, _ := sess.Context().Value(ctxUser{}).(*db.User)
	if user == nil {
		fmt.Fprintln(sess.Stderr(), "authentication error")
		sess.Exit(1)
		return
	}

	if !strings.EqualFold(sess.User(), menuLogin) {
		if container := s.accessibleVM(user, sess.User()); container != nil {
			s.connectToVM(sess, container)
			return
		}
	}

	if line := strings.TrimSpace(sess.RawCommand()); line != "" {
		s.runOneShot(sess, user, line)
		return
	}

	s.runMenu(sess, user)
}

// accessibleVM resolves an SSH login to an owned or explicitly granted VM, by display name
// or by the Incus name. It returns nil for anything else, including the empty
// login.
func (s *Server) accessibleVM(user *db.User, login string) *db.Container {
	if login == "" {
		return nil
	}
	container, err := s.db.ResolveAccessibleContainer(login, user.ID)
	if err != nil {
		return nil
	}
	return container
}

// connectToVM attaches the session to a VM the caller may use. A command given on
// the ssh command line runs inside that VM, as it would on any other SSH host.
func (s *Server) connectToVM(sess gssh.Session, container *db.Container) {
	if container.Status != "running" {
		fmt.Fprintf(sess.Stderr(), "VM %q is not running (status: %s)\n", container.Name, container.Status)
		sess.Exit(1)
		return
	}

	var command []string
	if line := strings.TrimSpace(sess.RawCommand()); line != "" {
		command = []string{"/bin/bash", "-lc", line}
	}

	if err := s.attach(sess.Context(), sess, container, command); err != nil {
		fmt.Fprintf(sess.Stderr(), "exec error: %v\n", err)
		sess.Exit(1)
		return
	}
	sess.Exit(0)
}

// attach runs a command inside a VM with the session's terminal wired to it.
// A nil command opens a login shell.
func (s *Server) attach(ctx context.Context, sess gssh.Session, container *db.Container, command []string) error {
	if user, _ := sess.Context().Value(ctxUser{}).(*db.User); user != nil {
		var cancel context.CancelFunc
		ctx, cancel = s.db.ContainerAccessContext(ctx, container, user.ID)
		defer cancel()
		if ctx.Err() != nil {
			return fmt.Errorf("VM access revoked")
		}
		stopClose := context.AfterFunc(ctx, func() { _ = sess.Close() })
		defer stopClose()
	}
	sr, ok := s.runtime.(runtime.ShellRuntime)
	if !ok {
		return fmt.Errorf("this runtime does not support interactive sessions")
	}

	ptyReq, winCh, isPTY := sess.Pty()

	initialCols, initialRows := uint16(80), uint16(24)
	if isPTY {
		initialCols = uint16(ptyReq.Window.Width)
		initialRows = uint16(ptyReq.Window.Height)
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

	if command == nil {
		command = []string{"/bin/bash"}
		if isPTY {
			command = []string{"/bin/bash", "-l"}
		}
	}

	env := map[string]string{}
	if isPTY {
		env["TERM"] = ptyReq.Term
		if env["TERM"] == "" {
			env["TERM"] = "xterm-256color"
		}
	}

	if err := sr.ExecInteractive(ctx, runtime.ExecInteractiveOpts{
		IncusName:   container.IncusName,
		Command:     command,
		Env:         env,
		Stdin:       sess,
		Stdout:      sess,
		InitialCols: initialCols,
		InitialRows: initialRows,
		Resize:      resizeCh,
		Done:        doneCh,
	}); err != nil {
		return err
	}

	select {
	case <-doneCh:
	case <-ctx.Done():
	}
	return nil
}

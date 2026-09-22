// Command demo renders the real dashboard and SSH gateway against sample data.
// It binds loopback only and never connects to Incus or an LLM provider.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/dashboard"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/sshgw"
	"golang.org/x/crypto/ssh"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	keyPath := flag.String("public-key", "", "ephemeral demo client's SSH public key")
	flag.Parse()
	pub, err := os.ReadFile(*keyPath)
	must(err)
	key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	must(err)
	dir, err := os.MkdirTemp("", "svkexe-media-")
	must(err)
	defer os.RemoveAll(dir)
	database, err := db.Open(filepath.Join(dir, "demo.db"))
	must(err)
	defer database.Close()
	owner, err := database.EnsureUser("demo", "demo@example.com")
	must(err)
	must(database.CreateSSHKey(&db.SSHKey{ID: "demo-key", UserID: owner.ID, PublicKey: string(pub), Fingerprint: ssh.FingerprintSHA256(key), Name: "Demo laptop"}))
	for i, name := range []string{"dev", "staging", "sandbox"} {
		status := "running"
		if name == "sandbox" {
			status = "stopped"
		}
		c := &db.Container{ID: name, Name: name, OwnerID: owner.ID, IncusName: "svkexe-demo-" + name, Status: status, IPAddress: "10.100.0.10", CPULimit: 2, MemoryMB: 2048, DiskGB: 10, Nesting: true}
		if i == 0 {
			c.CPULimit = 4
			c.MemoryMB = 4096
			c.DiskGB = 20
			c.AppPublic = true
		}
		must(database.CreateContainer(c))
		must(database.SetNestingApplied(c.ID, true))
		_, err = database.Exec("UPDATE containers SET created_at = '2026-09-22 10:00:00', ip_address = ? WHERE id = ?", []string{"10.100.0.10", "10.100.0.11", ""}[i], c.ID)
		must(err)
	}
	rt := offlineRuntime{}
	dash, err := dashboard.NewDashboard(database, rt, nil, "example.com", make([]byte, 32), nil, nil, nil)
	must(err)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "read-only media demo", http.StatusMethodNotAllowed)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, owner)))
		})
	})
	router.Route("/dashboard", dash.RegisterRoutes)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	must(err)
	gateway := sshgw.New(sshgw.Config{Addr: "127.0.0.1:22222", HostKey: signer, DB: database, Runtime: rt, Domain: "example.com"})
	defer gateway.Close()
	web := &http.Server{Addr: "127.0.0.1:18080", Handler: router, ReadHeaderTimeout: 5 * time.Second}
	defer web.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := web.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
			stop()
		}
	}()
	go func() {
		if err := gateway.ListenAndServe(); err != nil && ctx.Err() == nil {
			log.Print(err)
			stop()
		}
	}()
	log.Print("Media demo: http://127.0.0.1:18080/dashboard/vms (sample data, no Incus)")
	<-ctx.Done()
}

// Unsupported actions fail explicitly instead of pretending to operate a VM.
type offlineRuntime struct{}

var errOffline = errors.New("VM operations are unavailable in the media demo")

func (offlineRuntime) Create(context.Context, runtime.CreateOpts) (*runtime.Container, error) {
	return nil, errOffline
}
func (offlineRuntime) Get(context.Context, string) (*runtime.Container, error) {
	return nil, errOffline
}
func (offlineRuntime) List(context.Context, string) ([]*runtime.Container, error) {
	return nil, errOffline
}
func (offlineRuntime) Exec(context.Context, string, []string) ([]byte, error) { return nil, errOffline }
func (offlineRuntime) Start(context.Context, string) error                    { return errOffline }
func (offlineRuntime) Stop(context.Context, string) error                     { return errOffline }
func (offlineRuntime) Delete(context.Context, string) error                   { return errOffline }
func (offlineRuntime) Snapshot(context.Context, string, string) error         { return errOffline }
func (offlineRuntime) SetNesting(context.Context, string, bool) error         { return errOffline }

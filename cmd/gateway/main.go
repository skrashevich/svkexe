package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	gossh "golang.org/x/crypto/ssh"

	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/api"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/dnscheck"
	"github.com/skrashevich/svkexe/internal/llmproxy"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/proxy"
	"github.com/skrashevich/svkexe/internal/ratelimit"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
	"github.com/skrashevich/svkexe/internal/sshgw"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// shutdownGrace is how long in-flight requests have to finish before the
// gateway stops waiting for them. Every restart is a hole in service — the
// updater replaces the binary and restarts the unit — so this is sized to let
// ordinary requests land, not to outlast a streamed LLM turn.
const shutdownGrace = 5 * time.Second

func main() {
	// -version short-circuits before any env-based configuration is read, so
	// it works even in an environment missing required vars like DOMAIN.
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()
	if *showVersion {
		printVersion()
		os.Exit(0)
	}

	// Configuration from environment variables.
	listenAddr := getenv("GATEWAY_ADDR", ":8080")
	dbPath := getenv("GATEWAY_DB_PATH", "/var/lib/svkexe/gateway.db")
	encKeyHex := getenv("GATEWAY_ENC_KEY", "")
	incusSocket := getenv("INCUS_SOCKET", "/var/lib/incus/unix.socket")
	domain := getenv("DOMAIN", "")
	// The agent is told the address its VM answers on, built from this domain.
	picoclaw.Domain = domain
	secretsBasePath := getenv("SECRETS_BASE_PATH", "/var/lib/svkexe/secrets")
	sshAddr := getenv("SSH_ADDR", ":2222")
	sshHostKeyPath := getenv("SSH_HOST_KEY_PATH", "/var/lib/svkexe/ssh_host_key")
	rateLimitRPS := getenv("RATE_LIMIT_RPS", "10")
	rateLimitBurst := getenv("RATE_LIMIT_BURST", "20")
	// Custom domains are only routed once they demonstrably resolve here. A
	// deployment whose DOMAIN does not resolve to the gateway itself (behind a
	// load balancer, or NAT) names its public addresses explicitly instead.
	gatewayPublicIPs := getenv("GATEWAY_PUBLIC_IPS", "")
	openRouterKey := getenv("OPENROUTER_API_KEY", "")
	openRouterModels := getenv("OPENROUTER_MODELS", "anthropic/claude-sonnet-4,openai/gpt-4o,google/gemini-2.5-flash")
	llmInternalToken := getenv("LLM_INTERNAL_TOKEN", "")

	// Encryption key must be 32 bytes (AES-256).
	encKey, err := deriveEncKey(encKeyHex)
	if err != nil {
		log.Fatalf("encryption key: %v", err)
	}

	// Parse rate limit configuration.
	rps, err := strconv.ParseFloat(rateLimitRPS, 64)
	if err != nil || rps <= 0 {
		log.Printf("invalid RATE_LIMIT_RPS %q, using default 10", rateLimitRPS)
		rps = 10
	}
	burst, err := strconv.Atoi(rateLimitBurst)
	if err != nil || burst <= 0 {
		log.Printf("invalid RATE_LIMIT_BURST %q, using default 20", rateLimitBurst)
		burst = 20
	}
	rl := ratelimit.New(rps, burst)

	// Open database.
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Session cookie hardening: set Secure flag when deployed behind TLS.
	api.CookieSecure = strings.EqualFold(getenv("GATEWAY_COOKIE_SECURE", "0"), "1") ||
		strings.EqualFold(getenv("GATEWAY_COOKIE_SECURE", ""), "true")

	// Set cookie domain so sessions work across subdomains (e.g. picoclaw.vm.domain).
	if domain != "" {
		api.CookieDomain = "." + domain
	}

	// Bootstrap an admin account from env if requested (idempotent: updates the
	// password if the user already exists).
	if adminEmail := os.Getenv("BOOTSTRAP_ADMIN_EMAIL"); adminEmail != "" {
		adminPassword := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
		if adminPassword == "" {
			log.Printf("BOOTSTRAP_ADMIN_EMAIL set but BOOTSTRAP_ADMIN_PASSWORD is empty — skipping bootstrap")
		} else if err := bootstrapAdmin(database, adminEmail, adminPassword); err != nil {
			log.Fatalf("bootstrap admin: %v", err)
		}
	}

	// Purge expired sessions every hour.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			if n, err := database.DeleteExpiredSessions(); err != nil {
				log.Printf("expired session purge: %v", err)
			} else if n > 0 {
				log.Printf("expired session purge: removed %d rows", n)
			}
		}
	}()

	// Build runtime client.
	rt, err := runtime.NewIncusRuntime(incusSocket)
	if err != nil {
		log.Fatalf("init incus client: %v", err)
	}

	// Build key materializer.
	materializer := secrets.NewMaterializer(database, encKey, secretsBasePath)

	// Load or generate SSH host key.
	hostKey, err := loadOrGenerateHostKey(sshHostKeyPath)
	if err != nil {
		log.Fatalf("ssh host key: %v", err)
	}

	// Build LLM proxy config.
	var llmCfg *llmproxy.Config
	var picoclawLLM *picoclaw.LLMProxyConfig
	if openRouterKey != "" {
		models := strings.Split(openRouterModels, ",")
		llmCfg = &llmproxy.Config{
			APIKey:        openRouterKey,
			Models:        models,
			InternalToken: llmInternalToken,
		}
		log.Printf("LLM proxy enabled with %d models", len(models))
	}

	// Derive the LLM proxy URL for PicoClaw inside containers. An explicit
	// LLM_PROXY_URL names someone else's endpoint and is taken at its word; the
	// URL derived from DOMAIN is only real while this gateway serves /api/llm,
	// which needs OPENROUTER_API_KEY. Seeding models against a dead endpoint
	// would hand every VM a default model that cannot answer.
	llmProxyURL := getenv("LLM_PROXY_URL", "")
	if llmProxyURL == "" && domain != "" && llmCfg != nil {
		llmProxyURL = "https://" + domain + "/api/llm/v1"
	}
	if llmProxyURL == "" && domain != "" {
		log.Printf("LLM proxy disabled (no OPENROUTER_API_KEY): VMs get models only from their owners' own LLM keys")
	}
	if llmProxyURL != "" {
		var models []string
		for _, m := range strings.Split(openRouterModels, ",") {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		picoclawLLM = &picoclaw.LLMProxyConfig{
			BaseURL: llmProxyURL,
			Token:   llmInternalToken,
			Models:  models,
		}
	}

	// Build and start SSH gateway.
	sshGateway := sshgw.New(sshAddr, hostKey, database, rt, materializer, picoclawLLM)
	go func() {
		if err := sshGateway.ListenAndServe(); err != nil {
			log.Printf("SSH gateway stopped: %v", err)
		}
	}()

	// Self-update: the GitHub source and the local trigger paths are entirely
	// environment-driven, so a deployment that cannot self-update simply ends
	// up with a service that reports itself unavailable.
	updateSvc := updater.NewServiceFromEnv()

	// Build API server and container proxy.
	aliasVerifier := dnscheck.New(domain, strings.Split(gatewayPublicIPs, ","))
	apiSrv := api.NewServer(database, rt, encKey, domain, materializer, rl, llmCfg, picoclawLLM, updateSvc, aliasVerifier)
	containerProxy := proxy.New(database, rt, domain)

	// Top-level handler: route by Host header.
	// Subdomain requests (*.DOMAIN) go to ContainerProxy.
	// Everything else goes to the API server.
	topHandler := buildTopHandler(domain, apiSrv, containerProxy)

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           topHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return context.Background()
		},
	}

	// Start server in background.
	go func() {
		log.Printf("svkexe gateway listening on %s (domain=%s)", listenAddr, domain)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// Upgrade agents in running VMs; stopped VMs are handled on their next start.
	agentCtx, stopAgents := context.WithCancel(context.Background())
	defer stopAgents()
	// Only once that pass is done are the tasks handed to agents followed, so
	// the dashboard learns whether each one is still working, finished or
	// failed: a VM polled mid-upgrade is asked about an agent that is still
	// being installed and migrated.
	go func() {
		picoclaw.ReconcileRunning(agentCtx, database, rt, materializer, picoclawLLM)
		picoclaw.MonitorTasks(agentCtx, database, rt, picoclaw.TaskPollInterval)
	}()

	// Re-check routed custom domains, so a name whose owner repointed it stops
	// being served — and stops being a certificate this gateway renews — instead
	// of staying verified forever.
	go aliases.Reverify(agentCtx, database, rt, aliasVerifier, aliases.ReverifyInterval)

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	stopAgents()

	stopServer(httpServer, shutdownGrace)
	log.Println("stopped")
}

// stopServer takes the HTTP server down, giving requests already in flight
// grace to finish and closing whatever is still open when that runs out.
//
// Some connections never go idle by themselves: a VM's agent streaming a turn
// through the LLM proxy holds one for as long as the model keeps talking, and an
// open agent UI holds another. Waiting for those means the gateway is
// unreachable for the whole grace period, which is what turns an update into a
// visible outage — the updater replaces the binary and restarts the unit, so
// every second spent here is a second of 502s. Closing them is the cheaper end
// of the trade: an agent whose stream is cut retries it, and a browser
// reconnects.
func stopServer(srv *http.Server, grace time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		return
	}
	log.Printf("closing connections still open after %s", grace)
	if err := srv.Close(); err != nil {
		log.Printf("close listeners: %v", err)
	}
}

// hostRouter is what the top-level handler needs from the container proxy: it
// serves VM traffic, and it can say whether a host outside the gateway's own
// domain is a custom domain one of its VMs claims.
type hostRouter interface {
	http.Handler
	KnowsHost(host string) bool
}

// buildTopHandler returns an http.Handler that dispatches based on the Host header.
// Requests to *.domain are forwarded to the container proxy, as are custom
// domains an owner has pointed at their VM. All other requests are handled by
// the API server, which is what keeps an unrelated Host from being answered
// with somebody's workload.
func buildTopHandler(domain string, apiSrv http.Handler, cp hostRouter) http.Handler {
	subdomainSuffix := "." + domain
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port from host for comparison.
		if idx := strings.LastIndex(host, ":"); idx != -1 && strings.Count(host, ":") == 1 {
			host = host[:idx]
		}
		if domain != "" && strings.HasSuffix(host, subdomainSuffix) && host != domain {
			cp.ServeHTTP(w, r)
			return
		}
		// The gateway's own domain is the dashboard and the API, never an
		// alias, so it is checked first and never reaches the lookup.
		if host != domain && cp.KnowsHost(r.Host) {
			cp.ServeHTTP(w, r)
			return
		}
		apiSrv.ServeHTTP(w, r)
	})
}

// printVersion writes the build metadata in a human-readable form for the
// -version flag.
func printVersion() {
	info := version.Get()
	fmt.Printf("Version: %s\n", info.Version)
	fmt.Printf("Commit: %s\n", info.Commit)
	fmt.Printf("Build date: %s\n", info.BuildDate)
	fmt.Printf("PicoClaw version: %s\n", info.PicoClawVersion)
	fmt.Printf("Shelley commit: %s\n", info.ShelleyCommit)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadOrGenerateHostKey loads an ed25519 host key from path, or generates and saves one.
func loadOrGenerateHostKey(path string) (gossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("invalid PEM in %s", path)
		}
		signer, err := gossh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse host key: %w", err)
		}
		return signer, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	// Generate new ed25519 key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	pemBytes, err := marshalED25519PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create host key dir: %w", err)
	}
	if err := os.WriteFile(path, pemBytes, 0600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}
	log.Printf("generated new SSH host key at %s", path)

	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}
	return signer, nil
}

// marshalED25519PrivateKey encodes an ed25519 private key in OpenSSH PEM format.
func marshalED25519PrivateKey(key ed25519.PrivateKey) ([]byte, error) {
	// gossh can marshal it for us via MarshalPrivateKey.
	pemBlock, err := gossh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(pemBlock), nil
}

// bootstrapAdmin creates or updates the operator-managed admin account based
// on env vars. The password is always re-hashed so rotating the env var
// reliably resets the password on the next restart.
func bootstrapAdmin(database *db.DB, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	existing, err := database.GetUserByEmail(email)
	if err == nil {
		if err := database.SetUserPassword(existing.ID, string(hash)); err != nil {
			return err
		}
		if existing.Role != "admin" {
			existing.Role = "admin"
			if err := database.UpdateUser(existing); err != nil {
				return err
			}
		}
		log.Printf("bootstrap admin: refreshed password for %s", email)
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	u := &db.User{
		ID:           uuid.NewString(),
		Email:        email,
		Role:         "admin",
		PasswordHash: string(hash),
	}
	if err := database.CreateUser(u); err != nil {
		return err
	}
	log.Printf("bootstrap admin: created admin account %s", email)
	return nil
}

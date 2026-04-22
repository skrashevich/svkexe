package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
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

	"github.com/skrashevich/svkexe/internal/api"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/llmproxy"
	"github.com/skrashevich/svkexe/internal/proxy"
	"github.com/skrashevich/svkexe/internal/shelley"
	"github.com/skrashevich/svkexe/internal/ratelimit"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
	"github.com/skrashevich/svkexe/internal/sshgw"
)

func main() {
	// Configuration from environment variables.
	listenAddr := getenv("GATEWAY_ADDR", ":8080")
	dbPath := getenv("GATEWAY_DB_PATH", "/var/lib/svkexe/gateway.db")
	encKeyHex := getenv("GATEWAY_ENC_KEY", "")
	incusSocket := getenv("INCUS_SOCKET", "/var/lib/incus/unix.socket")
	domain := getenv("DOMAIN", "")
	secretsBasePath := getenv("SECRETS_BASE_PATH", "/var/lib/svkexe/secrets")
	sshAddr := getenv("SSH_ADDR", ":2222")
	sshHostKeyPath := getenv("SSH_HOST_KEY_PATH", "/var/lib/svkexe/ssh_host_key")
	rateLimitRPS := getenv("RATE_LIMIT_RPS", "10")
	rateLimitBurst := getenv("RATE_LIMIT_BURST", "20")
	openRouterKey := getenv("OPENROUTER_API_KEY", "")
	openRouterModels := getenv("OPENROUTER_MODELS", "anthropic/claude-sonnet-4,openai/gpt-4o,google/gemini-2.5-flash")
	llmInternalToken := getenv("LLM_INTERNAL_TOKEN", "")

	// Encryption key must be 32 bytes (AES-256).
	encKey := deriveEncKey(encKeyHex)

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

	// Set cookie domain so sessions work across subdomains (e.g. shelley.vm.domain).
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
	var shelleyLLM *shelley.LLMProxyConfig
	if openRouterKey != "" {
		models := strings.Split(openRouterModels, ",")
		llmCfg = &llmproxy.Config{
			APIKey:        openRouterKey,
			Models:        models,
			InternalToken: llmInternalToken,
		}
		log.Printf("LLM proxy enabled with %d models", len(models))
	}

	// Derive the LLM proxy URL for Shelley inside containers.
	// This is independent of OpenRouter — containers need it whenever
	// the gateway exposes an LLM endpoint.
	llmProxyURL := getenv("LLM_PROXY_URL", "")
	if llmProxyURL == "" && domain != "" {
		llmProxyURL = "https://" + domain + "/api/llm/v1"
	}
	if llmProxyURL != "" {
		// Use the first model from OPENROUTER_MODELS as Shelley's default.
		defaultModel := ""
		if models := strings.Split(openRouterModels, ","); len(models) > 0 {
			defaultModel = strings.TrimSpace(models[0])
		}
		shelleyLLM = &shelley.LLMProxyConfig{
			BaseURL:      llmProxyURL,
			Token:        llmInternalToken,
			DefaultModel: defaultModel,
		}
	}

	// Build and start SSH gateway.
	sshGateway := sshgw.New(sshAddr, hostKey, database, rt, materializer, shelleyLLM)
	go func() {
		if err := sshGateway.ListenAndServe(); err != nil {
			log.Printf("SSH gateway stopped: %v", err)
		}
	}()

	// Build API server and container proxy.
	apiSrv := api.NewServer(database, rt, encKey, domain, materializer, rl, llmCfg, shelleyLLM)
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

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	log.Println("stopped")
}

// buildTopHandler returns an http.Handler that dispatches based on the Host header.
// Requests to *.domain are forwarded to the container proxy.
// All other requests are handled by the API server.
func buildTopHandler(domain string, apiSrv http.Handler, cp http.Handler) http.Handler {
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
		apiSrv.ServeHTTP(w, r)
	})
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// deriveEncKey converts the hex env var to a 32-byte key.
// If empty or too short, falls back to a zeroed key (dev mode only).
func deriveEncKey(hex string) []byte {
	key := make([]byte, 32)
	copy(key, []byte(hex))
	return key
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

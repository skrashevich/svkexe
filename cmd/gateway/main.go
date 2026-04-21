package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
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

	"github.com/svkexe/platform/internal/api"
	"github.com/svkexe/platform/internal/db"
	"github.com/svkexe/platform/internal/proxy"
	"github.com/svkexe/platform/internal/ratelimit"
	"github.com/svkexe/platform/internal/runtime"
	"github.com/svkexe/platform/internal/secrets"
	"github.com/svkexe/platform/internal/sshgw"
	gossh "golang.org/x/crypto/ssh"
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

	// Build and start SSH gateway.
	sshGateway := sshgw.New(sshAddr, hostKey, database, rt)
	go func() {
		if err := sshGateway.ListenAndServe(); err != nil {
			log.Printf("SSH gateway stopped: %v", err)
		}
	}()

	// Build API server and container proxy.
	apiSrv := api.NewServer(database, rt, encKey, domain, materializer, rl)
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

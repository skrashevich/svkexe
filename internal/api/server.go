package api

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/svkexe/platform/internal/dashboard"
	"github.com/svkexe/platform/internal/db"
	"github.com/svkexe/platform/internal/metrics"
	"github.com/svkexe/platform/internal/proxy"
	"github.com/svkexe/platform/internal/ratelimit"
	"github.com/svkexe/platform/internal/runtime"
	"github.com/svkexe/platform/internal/secrets"
)

// Server holds the HTTP server dependencies.
type Server struct {
	db             *db.DB
	runtime        runtime.ContainerRuntime
	encKey         []byte
	domain         string
	router         chi.Router
	containerProxy *proxy.ContainerProxy
	materializer   *secrets.Materializer
	rateLimiter    *ratelimit.Limiter
}

// NewServer constructs a Server with the given dependencies and registers routes.
// domain is the base domain used for subdomain-based container routing (e.g. "example.com").
// materializer may be nil, in which case key materialization is skipped.
// rl may be nil, in which case rate limiting is disabled.
func NewServer(database *db.DB, rt runtime.ContainerRuntime, encKey []byte, domain string, materializer *secrets.Materializer, rl *ratelimit.Limiter) *Server {
	s := &Server{
		db:             database,
		runtime:        rt,
		encKey:         encKey,
		domain:         domain,
		containerProxy: proxy.New(database, rt, domain),
		materializer:   materializer,
		rateLimiter:    rl,
	}
	s.router = s.buildRouter()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Use(metrics.Middleware)

	// Unauthenticated endpoints. Keep these before the auth middleware is
	// mounted — order matters for chi.
	r.Get("/login", s.loginGet)
	r.Get("/logout", s.logoutPost)
	r.Get("/register", s.registerGet)
	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	// Mutating auth endpoints: apply the rate limiter when available to make
	// password brute-force expensive. The limiter keys by X-ExeDev-Userid
	// (not set pre-login) and falls back to RemoteAddr — which is what we want
	// for per-IP throttling of login attempts.
	r.Group(func(r chi.Router) {
		if s.rateLimiter != nil {
			r.Use(s.rateLimiter.Middleware)
		}
		r.Post("/login", s.loginPost)
		r.Post("/logout", s.logoutPost)
		r.Post("/register", s.registerPost)
	})

	// Everything below requires a valid session cookie.
	r.Group(func(r chi.Router) {
		r.Use(s.AuthMiddleware)
		if s.rateLimiter != nil {
			r.Use(s.rateLimiter.Middleware)
		}
		s.registerAuthedRoutes(r)
	})

	return r
}

func (s *Server) registerAuthedRoutes(r chi.Router) {

	r.Route("/api", func(r chi.Router) {
		// Current user
		r.Get("/me", s.getMe)

		// Container endpoints
		r.Get("/containers", s.listContainers)
		r.Post("/containers", s.createContainer)
		r.Route("/containers/{id}", func(r chi.Router) {
			r.Use(s.OwnershipMiddleware)
			r.Get("/", s.getContainer)
			r.Delete("/", s.deleteContainer)
			r.Post("/start", s.startContainer)
			r.Post("/stop", s.stopContainer)
			r.Post("/share", s.createSharedLink)
			r.Get("/shares", s.listSharedLinks)
		})

		// Shared link management
		r.Delete("/shares/{token}", s.revokeSharedLink)

		// API key endpoints
		r.Get("/keys", s.listKeys)
		r.Post("/keys", s.createKey)
		r.Delete("/keys/{id}", s.deleteKey)

		// SSH key endpoints
		r.Get("/ssh-keys", s.listSSHKeys)
		r.Post("/ssh-keys", s.createSSHKey)
		r.Delete("/ssh-keys/{id}", s.deleteSSHKey)

		// Admin endpoints
		r.Route("/admin", func(r chi.Router) {
			r.Use(s.AdminMiddleware)
			r.Get("/users", s.adminListUsers)
			r.Delete("/users/{id}", s.adminDeleteUser)
			r.Get("/containers", s.adminListContainers)
		})
	})

	// Dashboard routes
	d, err := dashboard.NewDashboard(s.db, s.runtime, s.materializer, s.domain, s.encKey)
	if err != nil {
		log.Fatalf("failed to initialize dashboard: %v", err)
	}
	r.Route("/dashboard", d.RegisterRoutes)
}

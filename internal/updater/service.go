package updater

import (
	"context"
	"strings"

	"github.com/skrashevich/svkexe/internal/version"
)

// Service bundles the update check and the update trigger into the single
// dependency the API server and dashboard need, so neither has to know that
// "check GitHub" and "run the update" are two unrelated mechanisms.
type Service struct {
	checker *Checker
	runner  *Runner
}

// NewService pairs a Checker and a Runner.
func NewService(cfg Config, rcfg RunnerConfig) *Service {
	return &Service{checker: NewChecker(cfg), runner: NewRunner(rcfg)}
}

// NewServiceFromEnv builds a Service from the SVKEXE_UPDATE_* environment
// variables documented on NewConfigFromEnv and NewRunnerFromEnv.
func NewServiceFromEnv() *Service {
	return &Service{checker: NewChecker(NewConfigFromEnv()), runner: NewRunnerFromEnv()}
}

// ParseForce interprets the "force" query-string flag both the REST API and the
// dashboard accept. Only the affirmative spellings a link or a curl user would
// carry count; anything else, including an absent value, leaves the checker's
// cached result in play.
func ParseForce(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// Version returns the build metadata of the running binary.
func (s *Service) Version() version.Info { return s.checker.local }

// Check compares the running binary against the newest upstream build.
func (s *Service) Check(ctx context.Context, force bool) (Status, error) {
	return s.checker.Status(ctx, force)
}

// Available reports whether this deployment can install updates itself, and why
// not when it cannot.
func (s *Service) Available() (bool, string) { return s.runner.Available() }

// State returns the progress of the most recent update.
func (s *Service) State() (RunState, error) { return s.runner.Status() }

// Start requests an update. It returns ErrUpdateInProgress or
// ErrUpdateUnavailable (both wrap-friendly) when the request cannot be placed.
func (s *Service) Start(ctx context.Context) error { return s.runner.Start(ctx) }

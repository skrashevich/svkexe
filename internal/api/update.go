package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// updaterDisabledReason is what the update endpoints report when the gateway
// was constructed without an updater.Service. That is a supported deployment
// (an image built without the self-update wiring), not a bug, so the endpoints
// answer with a reason instead of a 500.
const updaterDisabledReason = "self-update is not configured in this deployment"

// adminVersion handles GET /api/admin/version — the build metadata of the
// running binary.
//
// It answers 200 even without an updater: the version stamp is compiled into
// the binary, so an operator must be able to read it on a deployment that
// cannot update itself.
func (s *Server) adminVersion(w http.ResponseWriter, r *http.Request) {
	info := version.Get()
	if s.updater != nil {
		info = s.updater.Version()
	}
	writeJSON(w, http.StatusOK, info)
}

// adminUpdateCheck handles GET /api/admin/update/check[?force=1] — compares the
// running binary against the newest upstream build.
//
// A checker failure is 502 rather than 500: the request itself was well formed
// and the gateway is healthy; what failed is the upstream call to the GitHub
// API. That distinction matters when an operator is deciding whether to look at
// the gateway logs or at GitHub's status page.
func (s *Server) adminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		http.Error(w, updaterDisabledReason, http.StatusServiceUnavailable)
		return
	}

	status, err := s.updater.Check(r.Context(), updater.ParseForce(r.URL.Query().Get("force")))
	if err != nil {
		err = fmt.Errorf("update check: %w", err)
		log.Printf("admin update check failed: %v", err)
		// The checker already bounds the upstream response it quotes, so the
		// wrapped message is safe to hand back to an admin verbatim.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// adminUpdateStatus handles GET /api/admin/update/status — the progress of the
// most recent update run.
//
// The state lives in a file that outlives the gateway process (the update
// restarts it), so a read failure is a local 500: the gateway could not read
// its own state directory.
func (s *Server) adminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		http.Error(w, updaterDisabledReason, http.StatusServiceUnavailable)
		return
	}

	state, err := s.updater.State()
	if err != nil {
		log.Printf("admin update status failed: %v", fmt.Errorf("read update state: %w", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// adminUpdateStart handles POST /api/admin/update — requests an update.
//
// Success is 202 rather than 200: Start only places the request, the update
// itself runs out of process and restarts this gateway. A second request while
// one is running is 409, because the conflict is with the current state of the
// resource and the caller can retry once the run finishes. A deployment that
// cannot self-update answers 503, which marks the capability as missing here
// rather than the request as wrong.
func (s *Server) adminUpdateStart(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		http.Error(w, updaterDisabledReason, http.StatusServiceUnavailable)
		return
	}

	// An update restarts the whole platform and every VM session riding on it,
	// so record who asked for it before anything happens.
	if user := userFromCtx(r.Context()); user != nil {
		log.Printf("admin update requested by user %s (%s)", user.ID, user.Email)
	} else {
		log.Printf("admin update requested by unidentified session")
	}

	switch err := s.updater.Start(r.Context()); {
	case err == nil:
	case errors.Is(err, updater.ErrUpdateInProgress):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, updater.ErrUpdateUnavailable):
		// The sentinel is wrapped with the reason the runner determined, which
		// is the only actionable part of this response.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	default:
		log.Printf("admin update start failed: %v", fmt.Errorf("start update: %w", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	state, err := s.updater.State()
	if err != nil {
		// The update has been requested and is out of our hands; only the
		// progress read failed. Report what we know rather than turning a
		// successful start into an error the operator would retry.
		log.Printf("admin update started but state read failed: %v", fmt.Errorf("read update state: %w", err))
		state = updater.RunState{State: updater.StateRunning}
	}
	writeJSON(w, http.StatusAccepted, state)
}

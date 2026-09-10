package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/aliases"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// maxTLSCheckDomainLen bounds the raw ?domain= parameter. A hostname is at most
// 253 characters; the slack covers surrounding whitespace and a trailing dot.
const maxTLSCheckDomainLen = 256

// aliasRequest is the JSON body for POST /api/containers/{id}/aliases.
type aliasRequest struct {
	// Hostname is the custom domain the owner wants pointed at this VM.
	Hostname string `json:"hostname"`
}

// containerForAlias re-reads the VM the route is scoped to. OwnershipMiddleware
// has already proven the caller owns it.
func (s *Server) containerForAlias(w http.ResponseWriter, r *http.Request) (*dbpkg.Container, bool) {
	c, err := s.db.GetContainerByID(containerIDFromURL(r))
	if err != nil {
		http.Error(w, "container not found", http.StatusNotFound)
		return nil, false
	}
	return c, true
}

// writeAliasError maps the manager's failures onto the status the caller can
// act on: a bad name is the caller's to fix, a taken one needs a different
// choice, and anything else is ours.
func writeAliasError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aliases.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, dbpkg.ErrAliasHostnameTaken), errors.Is(err, dbpkg.ErrAliasAlreadyOnVM):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, aliases.ErrNotFound):
		http.Error(w, "alias not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// listAliases handles GET /api/containers/{id}/aliases.
func (s *Server) listAliases(w http.ResponseWriter, r *http.Request) {
	c, ok := s.containerForAlias(w, r)
	if !ok {
		return
	}
	list, err := s.aliases.List(c.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*dbpkg.ContainerAlias{}
	}
	writeJSON(w, http.StatusOK, list)
}

// createAlias handles POST /api/containers/{id}/aliases. A hostname whose DNS
// does not point here yet is still stored: the response reports why it is not
// verified so the owner can fix the record and re-check.
func (s *Server) createAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := s.containerForAlias(w, r)
	if !ok {
		return
	}
	var req aliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	alias, err := s.aliases.Add(r.Context(), c, req.Hostname)
	if err != nil {
		writeAliasError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, alias)
}

// verifyAlias handles POST /api/containers/{id}/aliases/{aliasID}/verify.
func (s *Server) verifyAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := s.containerForAlias(w, r)
	if !ok {
		return
	}
	alias, err := s.aliases.Recheck(r.Context(), c, chi.URLParam(r, "aliasID"))
	if err != nil {
		writeAliasError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, alias)
}

// deleteAlias handles DELETE /api/containers/{id}/aliases/{aliasID}.
func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := s.containerForAlias(w, r)
	if !ok {
		return
	}
	if err := s.aliases.Remove(r.Context(), c, chi.URLParam(r, "aliasID")); err != nil {
		writeAliasError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminListAliases handles GET /api/admin/aliases — every custom domain on the
// platform with the VM and account holding it, which is what an operator needs
// to judge a dispute over a name.
func (s *Server) adminListAliases(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListAllAliases()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*dbpkg.AliasWithOwner{}
	}
	writeJSON(w, http.StatusOK, list)
}

// adminReleaseAlias handles DELETE /api/admin/aliases/{hostname} — frees a
// hostname platform-wide so a different account can claim it.
//
// This is the recovery path for a name held by the wrong tenant. Verification
// proves a hostname points at this gateway, not who controls it, so the first
// account to verify a name keeps it; without this route the only way to undo
// that would be deleting the holder's VM or their entire account.
func (s *Server) adminReleaseAlias(w http.ResponseWriter, r *http.Request) {
	hostname := chi.URLParam(r, "hostname")
	removed, err := s.aliases.Release(r.Context(), hostname)
	if err != nil {
		if errors.Is(err, aliases.ErrNotFound) {
			http.Error(w, "no VM claims that hostname", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Taking somebody's domain away is an operator overriding a tenant, so it
	// leaves a trace naming who did it.
	admin := userFromCtx(r.Context())
	email := "unknown"
	if admin != nil {
		email = admin.Email
	}
	log.Printf("admin %s released hostname %q (%d claims removed)", email, hostname, removed)
	w.WriteHeader(http.StatusNoContent)
}

// tlsCheck answers Caddy's on-demand TLS "ask": may a certificate be issued for
// this hostname? It has to be reachable without a session, because Caddy asks
// during a TLS handshake that has no user behind it.
//
// Answering yes only for a verified alias is what keeps the gateway from being
// used to drive certificate requests for names nobody here controls.
func (s *Server) tlsCheck(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("domain")
	if strings.TrimSpace(raw) == "" {
		http.Error(w, "missing domain", http.StatusBadRequest)
		return
	}
	// Length is checked on the raw parameter, before any normalisation: a
	// hostname tops out at 253 characters, and the query string is otherwise
	// bounded only by the server's max header size, so normalising first would
	// let a caller decide how much this handler allocates.
	if len(raw) > maxTLSCheckDomainLen {
		http.Error(w, "not a custom domain served by this gateway", http.StatusNotFound)
		return
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	// Rejecting a malformed name before any lookup means a flood of random SNI
	// values costs a string check rather than a database query each.
	if _, err := dbpkg.ValidAliasHostname(host, s.domain); err != nil {
		http.Error(w, "not a custom domain served by this gateway", http.StatusNotFound)
		return
	}
	if _, err := s.db.GetVerifiedAliasByHostname(host); err != nil {
		http.Error(w, "not a custom domain served by this gateway", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

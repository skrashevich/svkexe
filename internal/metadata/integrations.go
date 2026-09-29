package metadata

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

const integrationPath = "/latest/meta-data/svkexe/integrations"

// Credential handling runs after source attribution, method checks and IMDS token validation.
func (s *Service) serveIntegrations(w http.ResponseWriter, r *http.Request, id *Identity) bool {
	path := cleanPath(r.URL.Path)
	if path != integrationPath && !strings.HasPrefix(path, integrationPath+"/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	svc := s.cfg.Integrations
	if svc == nil {
		s.fail(w, 404, "not found")
		return true
	}
	rest := strings.Trim(strings.TrimPrefix(path, integrationPath), "/")
	connections, err := svc.List(r.Context(), id.OwnerID)
	if err != nil {
		s.fail(w, 503, "metadata is temporarily unavailable")
		return true
	}
	if rest == "" {
		if len(connections) == 0 {
			s.fail(w, 404, "not found")
			return true
		}
		var b strings.Builder
		for _, c := range connections {
			b.WriteString(c.Provider + "/\n")
		}
		s.write(w, b.String())
		return true
	}
	parts := strings.Split(rest, "/")
	connected := false
	for _, c := range connections {
		if c.Provider == parts[0] {
			connected = true
		}
	}
	if !connected || len(parts) > 2 {
		s.fail(w, 404, "not found")
		return true
	}
	desc, err := svc.Descriptor(parts[0])
	if err != nil {
		s.fail(w, 404, "not found")
		return true
	}
	if len(parts) == 1 {
		s.write(w, strings.Join(desc.Credentials, "\n")+"\n")
		return true
	}
	if strings.HasSuffix(path, "/") {
		s.fail(w, 404, "not found")
		return true
	}
	if !s.tokens.valid(r.Header.Get(tokenHeader), id.ContainerID) {
		s.fail(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	value, err := svc.Credential(r.Context(), id.OwnerID, parts[0], parts[1])
	if errors.Is(err, sql.ErrNoRows) {
		s.fail(w, 404, "not found")
	} else if err != nil {
		s.fail(w, 503, "metadata is temporarily unavailable")
	} else {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
		} else {
			s.write(w, value)
		}
	}
	return true
}

package api

import (
	"net/http"
	"strings"

	"github.com/skrashevich/svkexe/internal/llmproxy"
	"github.com/skrashevich/svkexe/internal/secrets"
)

func (s *Server) llmGateway(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(token, "vm1.") {
		if s.llmProxy == nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/api/llm/v1/models":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", 405)
				return
			}
			s.llmProxy.ServeModels(w, r)
		case "/api/llm/v1/chat/completions":
			s.llmProxy.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}
	container, owner, ok := llmproxy.ParseVMToken(s.encKey, token)
	if !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "unauthorized", 401)
		return
	}
	c, err := s.db.GetContainerByID(container)
	if err != nil || c.OwnerID != owner {
		http.Error(w, "unauthorized", 401)
		return
	}
	models, err := secrets.NewMaterializer(s.db, s.encKey, "").ProviderModels(owner)
	if err != nil {
		http.Error(w, "LLM connections unavailable", 503)
		return
	}
	if len(models) == 0 {
		if s.llmProxy == nil {
			http.Error(w, "no LLM models configured", 503)
			return
		}
		s.llmProxy.ServeVM(w, r)
		return
	}
	chosen, err := s.db.UserDefaultModel(owner)
	if err != nil {
		http.Error(w, "LLM settings unavailable", 503)
		return
	}
	llmproxy.ServeConnections(w, r, models, chosen)
}

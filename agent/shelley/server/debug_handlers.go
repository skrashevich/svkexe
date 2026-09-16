package server

import (
	"io"
	"net/http"

	"shelley.exe.dev/ui"
)

// handleDebugConversationsPage serves the conversations list debug page
func (s *Server) handleDebugConversationsPage(w http.ResponseWriter, r *http.Request) {
	fsys := ui.Assets()
	file, err := fsys.Open("/conversations.html")
	if err != nil {
		http.Error(w, "conversations.html not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	io.Copy(w, file)
}

package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
)

func TestGuestWebSocketClosesAfterRevocation(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@test.example")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := database.EnsureUser("guest", "guest@test.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{ID: "vm", Name: "vm", IncusName: "vm", OwnerID: owner.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('vm','guest')`); err != nil {
		t.Fatal(err)
	}
	d := &Dashboard{db: database, runtime: &asyncShellRuntime{}}
	router := chi.NewRouter()
	router.Get("/vms/{id}/ws", func(w http.ResponseWriter, r *http.Request) {
		d.handleWS(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, guest)))
	})
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/vms/vm/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := database.RevokeContainerAccess(owner.ID, "vm", guest.ID); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("guest connection stayed open")
	} else if strings.Contains(err.Error(), "timeout") {
		t.Fatalf("revocation did not close connection: %v", err)
	}
}
func TestAccessRejectsSiblingOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "https://gateway.example/dashboard/vms/vm/access", nil)
	r.Header.Set("Origin", "https://malicious-vm.gateway.example")
	w := httptest.NewRecorder()
	if accessSameOrigin(w, r) || w.Code != 403 {
		t.Fatal("sibling workload can submit an invitation as the owner")
	}
}

package dashboard

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type asyncShellRuntime struct{ runtime.ContainerRuntime }

func (*asyncShellRuntime) ExecInteractive(_ context.Context, opts runtime.ExecInteractiveOpts) error {
	go func() {
		defer close(opts.Done)
		b := make([]byte, 64)
		if _, err := opts.Stdin.Read(b); err != nil {
			return
		}
		_, _ = io.WriteString(opts.Stdout, "terminal-command-finished")
	}()
	return nil
}
func TestShellWaitsForAsyncRuntime(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	user, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{ID: "vm", Name: "box", OwnerID: user.ID, IncusName: "box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d := &Dashboard{db: database, runtime: &asyncShellRuntime{}}
	r := chi.NewRouter()
	r.Get("/vms/{id}/ws", func(w http.ResponseWriter, r *http.Request) {
		d.handleWS(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, user)))
	})
	s := httptest.NewServer(r)
	defer s.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/vms/vm/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, []byte("echo test\n")); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.ReadMessage()
	if err != nil || string(b) != "terminal-command-finished" {
		t.Fatalf("body=%q err=%v", b, err)
	}
}

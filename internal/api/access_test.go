package api

import (
	"github.com/skrashevich/svkexe/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuestHTTPAccessMatrix(t *testing.T) {
	s, d, guest := newAuthTestServer(t, "guest@test.example", "password", db.GuestRole)
	if _, err := d.EnsureUser("owner", "owner@test.example"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared", "private"} {
		if err := d.CreateContainer(&db.Container{ID: id, Name: id, OwnerID: "owner", IncusName: id, Status: "running", IPAddress: "127.0.0.1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('shared',?)`, guest); err != nil {
		t.Fatal(err)
	}
	session, err := d.CreateSession(guest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/containers", 200}, {"GET", "/api/containers/shared", 200}, {"GET", "/api/containers/private", 403},
		{"GET", "/dashboard/vms", 200}, {"GET", "/dashboard/vms/list", 200}, {"GET", "/dashboard/vms/shared/shell", 200}, {"GET", "/dashboard/vms/private/shell", 403},
		{"GET", "/dashboard/vms/shared", 200}, {"GET", "/dashboard/vms/shared/card", 200}, {"GET", "/dashboard/vms/private", 403}, {"GET", "/dashboard/vms/private/card", 403},
		{"POST", "/api/containers", 403}, {"POST", "/api/containers/shared/start", 403}, {"DELETE", "/api/containers/shared", 403},
		{"POST", "/dashboard/vms", 403}, {"POST", "/dashboard/vms/shared/stop", 403}, {"GET", "/dashboard/vms/create", 403},
		{"GET", "/dashboard/vms/shared/access", 403}, {"POST", "/dashboard/vms/shared/access", 403},
		{"POST", "/api/containers/shared/share", 403}, {"GET", "/api/admin/users", 403}, {"GET", "/dashboard/system", 403},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (tc.path == "/dashboard/vms" || tc.path == "/dashboard/vms/shared" || tc.path == "/dashboard/vms/shared/card") && w.Code == 200 {
				for _, forbidden := range []string{"New VM", "/access", "/stop", "/publish", "/nesting", "/recreate", "private"} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Errorf("guest page contains %q", forbidden)
					}
				}
			}
			if tc.path == "/dashboard/vms/shared" && w.Code == 200 && !strings.Contains(w.Body.String(), `data-tab="overview"`) {
				t.Errorf("guest did not get the VM page: %s", w.Body.String())
			}
		})
	}
	if err := d.RevokeContainerAccess("owner", "shared", guest); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/dashboard/vms/shared/shell", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.Token})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("revoked shell: %d", w.Code)
	}
}

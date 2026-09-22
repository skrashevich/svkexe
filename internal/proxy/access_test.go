package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNamedGuestProxyAndRevocation(t *testing.T) {
	f := newRoutingFixture(t)
	if _, err := f.database.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('vm','intruder')`); err != nil {
		t.Fatal(err)
	}
	session, err := f.database.CreateSession("intruder")
	if err != nil {
		t.Fatal(err)
	}
	if w := f.get(t, "box.example.com", session.Token); w.Code != 200 {
		t.Fatalf("guest workload: %d %s", w.Code, w.Body.String())
	}
	f.assertNoIdentityLeak(t)
	// A stopped agent returns 503 only after authorization; a stranger is rejected
	// before this point. Actual proxy forwarding is covered by the workload above.
	if err := f.database.UpdateContainerStatus("vm", "stopped", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if w := f.get(t, "agent-box.example.com", session.Token); w.Code != 503 {
		t.Fatalf("guest agent auth: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "https://agent-box.example.com/upgrade", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	w := httptest.NewRecorder()
	f.proxy.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("guest upgrade: %d", w.Code)
	}
	if err := f.database.RevokeContainerAccess("owner", "vm", "intruder"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"box.example.com", "agent-box.example.com", "3000-box.example.com"} {
		if w := f.get(t, host, session.Token); w.Code != 404 {
			t.Fatalf("revoked %s: %d", host, w.Code)
		}
	}
}

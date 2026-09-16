package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/runtime"
)

type agentUpdateRuntime struct {
	runtime.ContainerRuntime
	runtime.FileRuntime
	commands []string
	artifact []byte
}

func (rt *agentUpdateRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	text := strings.Join(cmd, " ")
	rt.commands = append(rt.commands, text)
	if strings.Contains(text, "sha256sum") {
		return []byte(fmt.Sprintf("%x  /proc/1/exe", sha256.Sum256([]byte("old")))), nil
	}
	if strings.HasSuffix(text, " version") {
		return []byte(`{"version":"picoclaw-test","customized":true}`), nil
	}
	return nil, nil
}
func (rt *agentUpdateRuntime) PushFile(_ context.Context, _ string, _ string, data []byte) error {
	rt.artifact = data
	return nil
}

func TestAgentUpdateRoutes(t *testing.T) {
	f := newRoutingFixture(t)
	rt := &agentUpdateRuntime{}
	f.proxy.runtime = rt
	path := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(path, []byte("platform"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", path)
	intruder, err := f.database.CreateSession("intruder")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, token, origin string
		want                              int
	}{
		{"unauthenticated", "POST", "/upgrade", "", "", 401},
		{"wrong owner", "POST", "/upgrade", intruder.Token, "", 404},
		{"cross origin", "POST", "/upgrade", f.sessionID, "https://evil.example.com", 403},
		{"wrong method", "GET", "/upgrade", f.sessionID, "", 405},
		{"check", "GET", "/version-check", f.sessionID, "", 200},
		{"update", "POST", "/upgrade", f.sessionID, "https://agent-box.example.com", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rt.commands)
			req := httptest.NewRequest(tc.method, "https://agent-box.example.com"+tc.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			f.proxy.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want != 200 && len(rt.commands) != before {
				t.Fatal("rejected request executed guest commands")
			}
			if tc.name == "check" && !strings.Contains(w.Body.String(), `"has_update":true`) {
				t.Fatal(w.Body.String())
			}
		})
	}
	if string(rt.artifact) != "platform" {
		t.Fatal("wrong artifact")
	}
	if !strings.Contains(strings.Join(rt.commands, "\n"), "systemctl restart picoclaw.service") {
		t.Fatal("agent was not restarted")
	}
}

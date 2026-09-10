package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/metrics"
)

func TestVMOperationOutlivesServerWriteTimeout(t *testing.T) {
	for _, path := range []string{"/api/containers", "/api/containers/vm/start", "/dashboard/vms/vm/stop"} {
		t.Run(path, func(t *testing.T) {
			s := httptest.NewUnstartedServer(metrics.Middleware(vmOperationDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(100 * time.Millisecond)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "completed")
			}))))
			s.Config.WriteTimeout = 25 * time.Millisecond
			s.Start()
			defer s.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusCreated || string(body) != "completed" {
				t.Fatalf("status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
		})
	}
}

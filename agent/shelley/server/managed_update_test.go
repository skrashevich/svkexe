package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type rejectUpdateTransport struct{ t *testing.T }

func (rt rejectUpdateTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.t.Fatal("managed agent must not contact upstream for updates")
	return nil, nil
}

func TestManagedAgentUpdates(t *testing.T) {
	vc := NewVersionChecker()
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: rejectUpdateTransport{t}}
	t.Cleanup(func() { http.DefaultClient = previous })
	vc.cachedInfo = &VersionInfo{HasUpdate: true, ShouldNotify: true, LatestTag: "upstream"}
	vc.lastCheck = time.Now()
	for _, force := range []bool{false, true} {
		info, err := vc.Check(context.Background(), force)
		if err != nil {
			t.Fatal(err)
		}
		if info.HasUpdate || info.ShouldNotify || info.LatestTag != "" || info.DownloadURL != "" {
			t.Fatalf("upstream update advertised: %+v", info)
		}
	}
	if err := vc.DoUpgrade(t.Context()); err == nil || !strings.Contains(err.Error(), "managed externally") {
		t.Fatalf("expected platform-managed update error, got %v", err)
	}
}

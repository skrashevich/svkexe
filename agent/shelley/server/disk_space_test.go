package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/diskspace"
)

func initTestDiskSpace(t *testing.T, s *Server, available *uint64) *atomic.Int32 {
	t.Helper()
	calls := new(atomic.Int32)
	if err := s.initDiskSpace(t.Context(), func(path string) (uint64, error) {
		calls.Add(1)
		if path != s.db.Path() {
			t.Errorf("probe path = %q, want %q", path, s.db.Path())
		}
		return *available, nil
	}); err != nil {
		t.Fatal(err)
	}
	return calls
}

func checkDiskSpace(t *testing.T, m *diskSpaceMonitor) diskspace.DiskSpaceStatus {
	t.Helper()
	if err := m.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m.snapshot()
}

func TestDiskSpaceBoundaryAndEpisodes(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	var writes atomic.Int32
	database.Pool().OnCommit(func() { writes.Add(1) })
	available := diskspace.Threshold
	calls := initTestDiskSpace(t, s, &available)
	m := s.diskSpace
	if got := m.snapshot(); got.Active || got.AvailableBytes != available || got.EpisodeID != 0 || calls.Load() != 1 || writes.Load() != 0 {
		t.Fatalf("exact boundary/startup: %+v, probes=%d, writes=%d", got, calls.Load(), writes.Load())
	}

	available--
	low := checkDiskSpace(t, m)
	if !low.Active || low.Dismissed || low.EpisodeID != 1 || low.Revision != 1 || writes.Load() != 1 {
		t.Fatalf("below boundary: %+v, writes=%d", low, writes.Load())
	}
	for range 3 {
		available--
		got := checkDiskSpace(t, m)
		if got.AvailableBytes != available || got.EpisodeID != low.EpisodeID || got.Revision != low.Revision || writes.Load() != 1 {
			t.Fatalf("sustained low should only update cache: %+v, writes=%d", got, writes.Load())
		}
	}
	value, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "available_bytes") {
		t.Fatalf("persisted measurement: %s", value)
	}

	dismissed, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || !dismissed.Dismissed || dismissed.Revision != 2 || writes.Load() != 2 {
		t.Fatalf("dismiss: %+v, err=%v, writes=%d", dismissed, err, writes.Load())
	}
	again, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || again != dismissed || writes.Load() != 2 {
		t.Fatalf("idempotent dismissal: %+v, err=%v, writes=%d", again, err, writes.Load())
	}

	available = diskspace.Threshold
	recovered := checkDiskSpace(t, m)
	if recovered.Active || recovered.Dismissed || recovered.EpisodeID != low.EpisodeID || recovered.Revision != 3 || writes.Load() != 3 {
		t.Fatalf("recovery at exact boundary: %+v, writes=%d", recovered, writes.Load())
	}
	available++
	checkDiskSpace(t, m)
	if writes.Load() != 3 {
		t.Fatal("sustained healthy wrote settings")
	}

	available = 0
	later := checkDiskSpace(t, m)
	if !later.Active || later.Dismissed || later.EpisodeID <= low.EpisodeID || later.Revision != 4 || writes.Load() != 4 {
		t.Fatalf("later episode did not rearm: %+v, writes=%d", later, writes.Load())
	}
	stale, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || stale != later || writes.Load() != 4 {
		t.Fatalf("stale dismissal changed newer episode: %+v, err=%v", stale, err)
	}
}

func TestDiskSpaceDismissalPersistence(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := diskspace.Threshold - 1
	initTestDiskSpace(t, s, &available)
	dismissed, err := s.diskSpace.dismiss(t.Context(), s.diskSpace.snapshot().EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	path := database.Path()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.New(db.Config{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewServer(reopened, s.llmManager, s.toolSetConfig, s.logger, true, "predictable", "")
	var writes atomic.Int32
	reopened.Pool().OnCommit(func() { writes.Add(1) })
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.snapshot(); got != dismissed || writes.Load() != 0 {
		t.Fatalf("restart lost dismissal: %+v, want %+v; writes=%d", got, dismissed, writes.Load())
	}
	// A startup observation of recovery also rearms the next episode.
	available = diskspace.Threshold
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.snapshot(); got.Active || got.Dismissed || got.EpisodeID != dismissed.EpisodeID {
		t.Fatalf("startup recovery: %+v", got)
	}
	available--
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.snapshot(); !got.Active || got.Dismissed || got.EpisodeID <= dismissed.EpisodeID {
		t.Fatalf("next startup episode: %+v", got)
	}
}

func TestDiskSpaceProbeErrors(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	probeErr := errors.New("probe failed")
	if err := s.initDiskSpace(t.Context(), func(string) (uint64, error) { return 0, probeErr }); !errors.Is(err, probeErr) || s.diskSpace != nil {
		t.Fatalf("startup must fail, not report healthy: monitor=%v, err=%v", s.diskSpace, err)
	}
	available := diskspace.Threshold - 1
	initTestDiskSpace(t, s, &available)
	before := s.diskSpace.snapshot()
	s.diskSpace.probe = func(string) (uint64, error) { return 0, probeErr }
	if err := s.diskSpace.check(t.Context()); !errors.Is(err, probeErr) || s.diskSpace.snapshot() != before {
		t.Fatalf("probe error changed status: %+v, err=%v", s.diskSpace.snapshot(), err)
	}
	if _, err := diskAvailableBytes(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("statfs on missing path succeeded")
	}
	if _, err := diskAvailableBytes(s.db.Path()); err != nil {
		t.Fatalf("statfs on actual SQLite path: %v", err)
	}
}

func dismissDiskSpaceHTTP(t *testing.T, handler http.Handler, id uint64) diskspace.DiskSpaceStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/disk-space/dismiss", strings.NewReader(fmt.Sprintf(`{"episode_id":%d}`, id)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss: HTTP %d: %s", w.Code, w.Body.String())
	}
	var status diskspace.DiskSpaceStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDiskSpaceDismissRoute(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := diskspace.Threshold - 1
	initTestDiskSpace(t, s, &available)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	initial := s.diskSpace.snapshot()
	if stale := dismissDiskSpaceHTTP(t, mux, initial.EpisodeID+1); stale != initial {
		t.Fatalf("unknown ID changed status: %+v", stale)
	}
	dismissed := dismissDiskSpaceHTTP(t, mux, initial.EpisodeID)
	if !dismissed.Dismissed || dismissed != s.diskSpace.snapshot() {
		t.Fatalf("response is not authoritative: %+v", dismissed)
	}
	if got := dismissDiskSpaceHTTP(t, mux, initial.EpisodeID); got != dismissed {
		t.Fatalf("repeat dismissal: %+v", got)
	}
	available = diskspace.Threshold
	checkDiskSpace(t, s.diskSpace)
	available--
	later := checkDiskSpace(t, s.diskSpace)
	if got := dismissDiskSpaceHTTP(t, mux, initial.EpisodeID); got != later {
		t.Fatalf("stale route dismissal changed later episode: %+v", got)
	}
	for _, body := range []string{`{`, `{}`, `{"episode_id":0}`, `{"episode_id":-1}`, `{"episode_id":"1"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/disk-space/dismiss", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s: HTTP %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/disk-space/dismiss", nil))
	// Non-POST requests fall through to the server's catch-all handler.
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET dismissal: HTTP %d", w.Code)
	}

	before, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(fmt.Sprintf(`{"key":%q,"value":"{}"}`, diskSpaceSettingKey))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("internal key writable via /settings: HTTP %d", w.Code)
	}
	after, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil || after != before {
		t.Fatalf("internal setting was changed: %q, err=%v", after, err)
	}
}

func TestDiskSpaceDismissWriteError(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := diskspace.Threshold - 1
	initTestDiskSpace(t, s, &available)
	before := s.diskSpace.snapshot()
	// Fail at COMMIT, not in the settings statement itself.
	if err := database.Pool().Exec(t.Context(), `
		CREATE TABLE disk_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE disk_child (id INTEGER REFERENCES disk_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_disk_dismiss AFTER UPDATE ON settings WHEN NEW.key = 'internal.disk_space'
		BEGIN INSERT INTO disk_child VALUES (1); END;
	`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/disk-space/dismiss", strings.NewReader(`{"episode_id":1}`)))
	if w.Code != http.StatusInternalServerError || s.diskSpace.snapshot() != before {
		t.Fatalf("failed commit reported a saved dismissal: HTTP %d, status=%+v", w.Code, s.diskSpace.snapshot())
	}
	initTestDiskSpace(t, s, &available)
	if s.diskSpace.snapshot() != before {
		t.Fatal("failed commit persisted dismissal")
	}
}

// Read complete SSE frames rather than polling or sleeping. Each HTTP request
// has a deadline so a missing snapshot/update fails instead of hanging.
func openDiskSpaceStream(t *testing.T, url string) (*bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream HTTP %d", resp.StatusCode)
	}
	closeStream := func() { cancel(); resp.Body.Close() }
	t.Cleanup(closeStream)
	return bufio.NewScanner(resp.Body), closeStream
}

func readDiskSpaceFrame(t *testing.T, scanner *bufio.Scanner) diskspace.DiskSpaceStatus {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame StreamResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.DiskSpaceStatus != nil {
			if frame.ConversationID != "" {
				t.Fatalf("disk event is not global: %+v", frame)
			}
			return *frame.DiskSpaceStatus
		}
	}
	t.Fatalf("missing disk frame: %v", scanner.Err())
	return diskspace.DiskSpaceStatus{}
}

func TestDiskSpaceStreamSnapshotAndGlobalDelivery(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	available := diskspace.Threshold
	calls := initTestDiskSpace(t, s, &available)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Each new stream samples once (probe count +1) so an idle Shelley shows
	// the current state on page load, and the snapshot reflects that sample.
	available++
	a, closeA := openDiskSpaceStream(t, httpServer.URL+"/api/stream2")
	defer closeA()
	initial := readDiskSpaceFrame(t, a)
	if initial != s.diskSpace.snapshot() || initial.AvailableBytes != available || calls.Load() != 2 {
		t.Fatalf("initial snapshot: %+v, probes=%d", initial, calls.Load())
	}
	// Also cover caught-up reconnects, where no initial list patch is sent.
	s.conversationListStream.mu.Lock()
	hash := s.conversationListStream.currentHash
	s.conversationListStream.mu.Unlock()
	available--
	b, closeB := openDiskSpaceStream(t, httpServer.URL+"/api/stream2?conversation_list_hash="+hash)
	defer closeB()
	if got := readDiskSpaceFrame(t, b); got.AvailableBytes != available || calls.Load() != 3 {
		t.Fatalf("reconnect snapshot: %+v, probes=%d", got, calls.Load())
	}
	// A healthy sample on connect is not broadcast to existing subscribers.
	assertBoth := func(want diskspace.DiskSpaceStatus) {
		t.Helper()
		for _, scanner := range []*bufio.Scanner{a, b} {
			if got := readDiskSpaceFrame(t, scanner); got != want {
				t.Fatalf("global transition: %+v, want %+v", got, want)
			}
		}
	}
	available--
	low := checkDiskSpace(t, s.diskSpace)
	assertBoth(low)
	// Sustained low while visible pushes the fresh number on the same revision.
	available--
	refreshed := checkDiskSpace(t, s.diskSpace)
	if refreshed.Revision != low.Revision || refreshed.AvailableBytes != available {
		t.Fatalf("refresh: %+v", refreshed)
	}
	assertBoth(refreshed)
	dismissed := dismissDiskSpaceHTTP(t, mux, low.EpisodeID)
	assertBoth(dismissed)
	closeB()
	b, closeB = openDiskSpaceStream(t, httpServer.URL+"/api/stream2?conversation_list_hash="+hash)
	defer closeB()
	// Dismissed: the connect sample refreshes bytes silently; the snapshot
	// carries the dismissed episode and no frame reaches the other stream.
	if got := readDiskSpaceFrame(t, b); !got.Dismissed || got.EpisodeID != dismissed.EpisodeID || got.Revision != dismissed.Revision || calls.Load() != 6 {
		t.Fatalf("dismissed reconnect: %+v, probes=%d", got, calls.Load())
	}
	available = diskspace.Threshold
	assertBoth(checkDiskSpace(t, s.diskSpace))
	available--
	assertBoth(checkDiskSpace(t, s.diskSpace))
}

type diskSpaceLogWriter struct{ lines chan string }

func (w diskSpaceLogWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

// refreshDiskSpace is the end-of-turn hook: it samples once, logs probe
// errors without clearing a live warning, and is a no-op before init.
func TestDiskSpaceRefreshAtTurnEnd(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	s.refreshDiskSpace(t.Context()) // uninitialized: must not panic
	available := diskspace.Threshold
	calls := initTestDiskSpace(t, s, &available)
	available--
	s.refreshDiskSpace(t.Context())
	if got := s.diskSpace.snapshot(); !got.Active || calls.Load() != 2 {
		t.Fatalf("turn-end refresh did not sample: %+v, probes=%d", got, calls.Load())
	}
	before := s.diskSpace.snapshot()
	s.diskSpace.probe = func(string) (uint64, error) { return 0, errors.New("turn-end probe failed") }
	lines := make(chan string, 1)
	s.logger = slog.New(slog.NewTextHandler(diskSpaceLogWriter{lines}, nil))
	s.refreshDiskSpace(t.Context())
	if line := <-lines; !strings.Contains(line, "turn-end probe failed") {
		t.Fatalf("missing error log: %s", line)
	}
	if s.diskSpace.snapshot() != before {
		t.Fatal("probe error cleared warning")
	}

	// Dismissed: sustained-low samples are silent; the next event is recovery.
	next, _ := s.streamPub.SubscribeWithStatus(t.Context(), -1)
	s.diskSpace.probe = func(string) (uint64, error) { return available, nil }
	if _, err := s.diskSpace.dismiss(t.Context(), before.EpisodeID); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(); !ok || ev.DiskSpaceStatus == nil || !ev.DiskSpaceStatus.Dismissed {
		t.Fatalf("expected dismissal event, got %+v", ev)
	}
	available--
	s.refreshDiskSpace(t.Context())
	available = diskspace.Threshold
	s.refreshDiskSpace(t.Context())
	if ev, ok := next(); !ok || ev.DiskSpaceStatus == nil || ev.DiskSpaceStatus.Active {
		t.Fatalf("expected recovery event with no refresh leak before it, got %+v", ev)
	}
}

func TestDiskSpaceCriticalEscalation(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := diskspace.Threshold - 1
	initTestDiskSpace(t, s, &available)
	m := s.diskSpace
	low := m.snapshot()
	if low.Critical {
		t.Fatalf("low is not critical: %+v", low)
	}
	if _, err := m.dismiss(t.Context(), low.EpisodeID); err != nil {
		t.Fatal(err)
	}

	// Exactly the critical boundary is still just low: dismissed stays.
	available = diskspace.CriticalThreshold
	if got := checkDiskSpace(t, m); got.Critical || !got.Dismissed || got.Revision != 2 {
		t.Fatalf("at critical boundary: %+v", got)
	}
	// Below it: escalate once. Same episode, un-dismissed, persisted.
	available--
	crit := checkDiskSpace(t, m)
	if !crit.Critical || crit.Dismissed || crit.EpisodeID != low.EpisodeID || crit.Revision != 3 {
		t.Fatalf("critical escalation: %+v", crit)
	}
	value, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil || !strings.Contains(value, `"critical":true`) {
		t.Fatalf("critical not persisted: %s, err=%v", value, err)
	}
	// Dismiss critical, then climb back above 500 MB: latched, no re-show.
	if _, err := m.dismiss(t.Context(), crit.EpisodeID); err != nil {
		t.Fatal(err)
	}
	available = diskspace.Threshold - 1
	if got := checkDiskSpace(t, m); !got.Critical || !got.Dismissed || got.Revision != 4 {
		t.Fatalf("critical must latch within the episode: %+v", got)
	}
	// Dropping again does not escalate a second time.
	available = 0
	if got := checkDiskSpace(t, m); !got.Dismissed || got.Revision != 4 {
		t.Fatalf("critical escalated twice: %+v", got)
	}
	// Recovery clears everything; a fresh low episode starts non-critical.
	available = diskspace.Threshold
	if got := checkDiskSpace(t, m); got.Active || got.Critical || got.Dismissed || got.Revision != 5 {
		t.Fatalf("recovery: %+v", got)
	}
	available = diskspace.Threshold - 1
	if got := checkDiskSpace(t, m); !got.Active || got.Critical || got.EpisodeID != low.EpisodeID+1 {
		t.Fatalf("new episode: %+v", got)
	}
	// Straight from healthy to critical: one transition, critical from the start.
	available = diskspace.Threshold
	checkDiskSpace(t, m)
	available = 0
	if got := checkDiskSpace(t, m); !got.Active || !got.Critical || got.Dismissed || got.EpisodeID != low.EpisodeID+2 {
		t.Fatalf("direct critical: %+v", got)
	}

	// Persistence: critical survives a restart with the dismissal intact.
	if _, err := m.dismiss(t.Context(), low.EpisodeID+2); err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(database, s.llmManager, s.toolSetConfig, s.logger, true, "predictable", "")
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.snapshot(); !got.Critical || !got.Dismissed || got.EpisodeID != low.EpisodeID+2 {
		t.Fatalf("after restart: %+v", got)
	}
}

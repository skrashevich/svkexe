package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/sys/unix"
	"shelley.exe.dev/server/diskspace"
	"shelley.exe.dev/subpub"
)

const (
	diskSpaceSettingKey = "internal.disk_space"
)

// Persist only episode transitions and dismissal, never periodic measurements.
type diskSpaceEpisode struct {
	EpisodeID uint64 `json:"episode_id"`
	Revision  uint64 `json:"revision"`
	Active    bool   `json:"active"`
	Critical  bool   `json:"critical"`
	Dismissed bool   `json:"dismissed"`
}

type diskSpaceMonitor struct {
	mu     sync.Mutex // serializes observations, persistence, publishing and subscription
	server *Server
	probe  func(string) (uint64, error)
	status diskspace.DiskSpaceStatus
}

func diskAvailableBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// initDiskSpace runs synchronously before serving. Route-only tests can inject
// a probe here and call check directly, without a worker or any sleeps.
func (s *Server) initDiskSpace(ctx context.Context, probe func(string) (uint64, error)) error {
	value, err := s.db.GetSetting(ctx, diskSpaceSettingKey)
	if err != nil {
		return fmt.Errorf("load disk space episode: %w", err)
	}
	var episode diskSpaceEpisode
	if value != "" {
		if err := json.Unmarshal([]byte(value), &episode); err != nil {
			return fmt.Errorf("decode disk space episode: %w", err)
		}
	}
	m := &diskSpaceMonitor{
		server: s,
		probe:  probe,
		status: diskspace.DiskSpaceStatus{
			EpisodeID: episode.EpisodeID,
			Revision:  episode.Revision,
			Active:    episode.Active,
			Critical:  episode.Critical,
			Dismissed: episode.Dismissed,
		},
	}
	if err := m.check(ctx); err != nil {
		return err
	}
	s.diskSpace = m
	return nil
}

func (m *diskSpaceMonitor) snapshot() diskspace.DiskSpaceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *diskSpaceMonitor) check(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	available, err := m.probe(m.server.db.Path())
	if err != nil {
		// Unknown is not recovery: retain the last successful observation.
		return err
	}
	next := m.status
	next.AvailableBytes = available
	next.Active = available < diskspace.Threshold
	// Critical latches within an episode: hovering around the line must not
	// re-show the notice, so only recovery clears it.
	next.Critical = next.Active && (m.status.Critical || available < diskspace.CriticalThreshold)
	if next.Active == m.status.Active && next.Critical == m.status.Critical {
		m.status = next
		// Visible notice: push the fresh number. Same revision, nothing persisted.
		if next.Active && !next.Dismissed {
			m.server.streamPub.Broadcast(StreamResponse{DiskSpaceStatus: &next})
		}
		return nil
	}
	// Entering low, entering critical or recovering each un-dismiss: the
	// user gets one fresh notice per escalation.
	next.Dismissed = false
	if next.Active && !m.status.Active {
		next.EpisodeID++
	}
	return m.transition(ctx, next)
}

// transition requires mu. Publish only durable transitions, in commit order.
func (m *diskSpaceMonitor) transition(ctx context.Context, next diskspace.DiskSpaceStatus) error {
	next.Revision++
	value, err := json.Marshal(diskSpaceEpisode{
		EpisodeID: next.EpisodeID,
		Revision:  next.Revision,
		Active:    next.Active,
		Critical:  next.Critical,
		Dismissed: next.Dismissed,
	})
	if err != nil {
		return err
	}
	if err := m.server.db.SetSetting(ctx, diskSpaceSettingKey, string(value)); err != nil {
		return fmt.Errorf("persist disk space episode: %w", err)
	}
	m.status = next
	m.server.streamPub.Broadcast(StreamResponse{DiskSpaceStatus: &next})
	return nil
}

func (m *diskSpaceMonitor) dismiss(ctx context.Context, episodeID uint64) (diskspace.DiskSpaceStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Active && !m.status.Dismissed && m.status.EpisodeID == episodeID {
		next := m.status
		next.Dismissed = true
		if err := m.transition(ctx, next); err != nil {
			return m.status, err
		}
	}
	return m.status, nil
}

// refreshDiskSpace runs when an agent turn ends: that is when the disk most
// likely just changed. Together with the startup check this is the only
// sampling; one statfs, no DB write or broadcast unless the threshold was
// crossed.
func (s *Server) refreshDiskSpace(ctx context.Context) {
	if s.diskSpace == nil {
		return
	}
	if err := s.diskSpace.check(ctx); err != nil {
		s.logger.Error("Disk space check failed", "error", err)
	}
}

// Sample once per new stream (a page load or reconnect), so someone opening an
// idle Shelley sees the current state, then subscribe and capture the snapshot
// under the publishing lock. Any transition the sample caused was broadcast
// before this subscriber existed, so it arrives only via the snapshot; events
// queued afterwards are never older than it.
func (s *Server) subscribeStream(ctx context.Context) (func() (StreamResponse, bool), *subpub.SubscriptionStatus, *diskspace.DiskSpaceStatus) {
	s.refreshDiskSpace(ctx)
	var snapshot *diskspace.DiskSpaceStatus
	if s.diskSpace != nil {
		s.diskSpace.mu.Lock()
		defer s.diskSpace.mu.Unlock()
		current := s.diskSpace.status
		snapshot = &current
	}
	next, status := s.streamPub.SubscribeWithStatus(ctx, -1)
	return next, status, snapshot
}

func (s *Server) handleDismissDiskSpace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EpisodeID uint64 `json:"episode_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EpisodeID == 0 {
		http.Error(w, "A positive episode_id is required", http.StatusBadRequest)
		return
	}
	if s.diskSpace == nil {
		http.Error(w, "Disk space monitor not initialized", http.StatusServiceUnavailable)
		return
	}
	status, err := s.diskSpace.dismiss(r.Context(), req.EpisodeID)
	if err != nil {
		s.logger.Error("Failed to dismiss disk space notice", "error", err)
		http.Error(w, "Failed to dismiss disk space notice", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

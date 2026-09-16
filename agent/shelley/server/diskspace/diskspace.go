// Package diskspace holds the wire type and thresholds for the low disk
// space notice. It is a leaf so cmd/go2ts can import it without pulling in
// the server package.
package diskspace

const (
	// Threshold starts a low-space episode when available bytes fall below it.
	Threshold = uint64(2 << 30)
	// CriticalThreshold escalates an episode. Escalation clears any earlier
	// dismissal exactly once and then latches until the episode recovers.
	CriticalThreshold = uint64(500 << 20)
)

// DiskSpaceStatus is the last successful observation of the database filesystem.
// Revision orders transitions, including HTTP dismissal responses versus SSE.
// AvailableBytes is cached, not persisted, and may change within one revision.
type DiskSpaceStatus struct {
	EpisodeID      uint64 `json:"episode_id"`
	Revision       uint64 `json:"revision"`
	Active         bool   `json:"active"`
	Critical       bool   `json:"critical"`
	Dismissed      bool   `json:"dismissed"`
	AvailableBytes uint64 `json:"available_bytes"`
}

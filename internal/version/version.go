// Package version exposes the build metadata stamped into the gateway binary.
//
// The values are set at link time by the Makefile:
//
//	-X github.com/skrashevich/svkexe/internal/version.Version=v1.2.3
//
// A binary built without those flags still reports usable placeholders so the
// update checker can tell "unknown local build" from "known commit".
package version

// Build metadata, overridden via -ldflags -X at build time.
var (
	// Version is the human-readable release identifier, typically the output
	// of `git describe --tags --always --dirty`.
	Version = "dev"
	// Commit is the full git commit SHA the binary was built from.
	Commit = "unknown"
	// BuildDate is the RFC3339 UTC timestamp of the build.
	BuildDate = "unknown"
	// PicoClawVersion mirrors PICOCLAW_VERSION from agent/upstream.env.
	PicoClawVersion = "unknown"
	// ShelleyCommit mirrors SHELLEY_COMMIT from agent/upstream.env.
	ShelleyCommit = "unknown"
)

// Info is a snapshot of the build metadata, safe to serialize to JSON.
type Info struct {
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	BuildDate       string `json:"buildDate"`
	PicoClawVersion string `json:"picoclawVersion"`
	ShelleyCommit   string `json:"shelleyCommit"`
}

// Get returns the current build metadata.
func Get() Info {
	return Info{
		Version:         Version,
		Commit:          Commit,
		BuildDate:       BuildDate,
		PicoClawVersion: PicoClawVersion,
		ShelleyCommit:   ShelleyCommit,
	}
}

// IsDev reports whether the binary lacks a real commit stamp. Update checks
// cannot compare a dev build against a remote commit, so they say so instead
// of claiming an update is available.
func (i Info) IsDev() bool {
	return i.Commit == "" || i.Commit == "unknown"
}

// Short renders the version in the compact "v1.2.3 (abcdef1)" form used by the
// dashboard and the -version flag.
func (i Info) Short() string {
	if i.IsDev() {
		return i.Version
	}
	c := i.Commit
	if len(c) > 7 {
		c = c[:7]
	}
	return i.Version + " (" + c + ")"
}

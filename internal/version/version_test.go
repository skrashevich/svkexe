package version

import "testing"

// TestGetDefaults verifies the package-level vars retain their compiled-in
// placeholders (a binary built without -ldflags -X still reports usable
// values) and that Get() never surfaces an empty field.
func TestGetDefaults(t *testing.T) {
	info := Get()

	if info.Version != "dev" {
		t.Errorf("Version = %q, want %q", info.Version, "dev")
	}
	if info.Commit != "unknown" {
		t.Errorf("Commit = %q, want %q", info.Commit, "unknown")
	}

	fields := map[string]string{
		"Version":         info.Version,
		"Commit":          info.Commit,
		"BuildDate":       info.BuildDate,
		"PicoClawVersion": info.PicoClawVersion,
		"ShelleyCommit":   info.ShelleyCommit,
	}
	for name, v := range fields {
		if v == "" {
			t.Errorf("field %s is empty", name)
		}
	}
}

// TestIsDev covers the commit values that distinguish a dev build (no real
// commit stamp) from one built with a real git SHA.
func TestIsDev(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		want   bool
	}{
		{"unknown commit", "unknown", true},
		{"empty commit", "", true},
		{"real sha", "abcdef1234567890abcdef1234567890abcdef12", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := Info{Commit: tt.commit}
			if got := info.IsDev(); got != tt.want {
				t.Errorf("IsDev() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShort covers the compact rendering used by the dashboard and the
// -version flag: a stamped build truncates its commit to 7 characters, while
// a dev build (no real commit) renders just the version.
func TestShort(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "stamped build truncates commit to 7 chars",
			info: Info{Version: "v1.2.3", Commit: "abcdef1234567890abcdef1234567890abcdef12"},
			want: "v1.2.3 (abcdef1)",
		},
		{
			name: "dev build renders version only",
			info: Info{Version: "dev", Commit: "unknown"},
			want: "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.Short(); got != tt.want {
				t.Errorf("Short() = %q, want %q", got, tt.want)
			}
		})
	}
}
